package parser

import (
	"fmt"
	"os"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// ParseFile parses a file and extracts symbols. Routing:
//
//  1. Declarative formats (yaml, terraform, dockerfile, json) → line-based parser
//  2. Languages with a native tree-sitter grammar (go/ts/js/python/kotlin/java)
//     → tree-sitter
//  3. Everything else → universal-ctags fallback (if installed on PATH)
func ParseFile(path, lang string) ([]ExtractedSymbol, error) {
	// Declarative formats — no tree-sitter
	switch lang {
	case "yaml", "terraform", "dockerfile", "json":
		return parseDeclarative(path, lang)
	}

	// Native tree-sitter path for languages we have grammars for.
	if _, ok := languages[lang]; ok {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		language, err := GetLanguage(lang)
		if err != nil {
			return nil, err
		}

		parser := tree_sitter.NewParser()
		defer parser.Close()
		if err := parser.SetLanguage(language); err != nil {
			return nil, fmt.Errorf("set language %s: %w", lang, err)
		}

		tree := parser.Parse(src, nil)
		defer tree.Close()
		root := tree.RootNode()

		switch lang {
		case "go":
			return extractGo(root, src), nil
		case "typescript", "javascript":
			return extractTypeScript(root, src), nil
		case "python":
			return extractPython(root, src), nil
		case "kotlin":
			return extractKotlin(root, src), nil
		case "java":
			return extractJava(root, src), nil
		}
	}

	// Fallback: universal-ctags handles ~90 languages out of the box.
	// Returns empty slice (not error) if ctags is not installed — the
	// indexer then treats this file as "detected but not parsed."
	return parseCtags(path)
}

func extractGo(root *tree_sitter.Node, src []byte) []ExtractedSymbol {
	var syms []ExtractedSymbol

	for i := uint(0); i < root.ChildCount(); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		nodeType := child.Kind()

		var doc string
		if i > 0 {
			prev := root.Child(i - 1)
			if prev != nil && prev.Kind() == "comment" {
				doc = nodeText(prev, src)
			}
		}

		switch nodeType {
		case "function_declaration":
			name := childFieldText(child, "name", src)
			params := childFieldText(child, "parameters", src)
			result := childFieldText(child, "result", src)
			sig := "func " + name + params
			if result != "" {
				sig += " " + result
			}
			refs := extractCallRefs(child, src)
			syms = append(syms, ExtractedSymbol{
				Name:       name,
				Kind:       "function",
				Line:       int(child.StartPosition().Row) + 1,
				EndLine:    int(child.EndPosition().Row) + 1,
				Signature:  sig,
				DocComment: doc,
				References: refs,
			})

		case "method_declaration":
			name := childFieldText(child, "name", src)
			receiver := childFieldText(child, "receiver", src)
			params := childFieldText(child, "parameters", src)
			result := childFieldText(child, "result", src)
			sig := "func " + receiver + " " + name + params
			if result != "" {
				sig += " " + result
			}
			parentName := extractReceiverType(receiver)
			refs := extractCallRefs(child, src)
			syms = append(syms, ExtractedSymbol{
				Name:       name,
				Kind:       "method",
				Line:       int(child.StartPosition().Row) + 1,
				EndLine:    int(child.EndPosition().Row) + 1,
				Signature:  sig,
				DocComment: doc,
				ParentName: parentName,
				References: refs,
			})

		case "type_declaration":
			for j := uint(0); j < child.ChildCount(); j++ {
				spec := child.Child(j)
				if spec == nil || spec.Kind() != "type_spec" {
					continue
				}
				name := childFieldText(spec, "name", src)
				typeNode := spec.ChildByFieldName("type")
				kind := "type"
				sig := "type " + name
				if typeNode != nil {
					switch typeNode.Kind() {
					case "struct_type":
						kind = "struct"
						sig += " struct{...}"
					case "interface_type":
						kind = "interface"
						sig += " interface{...}"
					default:
						sig += " " + nodeText(typeNode, src)
					}
				}
				syms = append(syms, ExtractedSymbol{
					Name:       name,
					Kind:       kind,
					Line:       int(spec.StartPosition().Row) + 1,
					EndLine:    int(spec.EndPosition().Row) + 1,
					Signature:  sig,
					DocComment: doc,
				})
			}
		}
	}
	return syms
}

func extractTypeScript(root *tree_sitter.Node, src []byte) []ExtractedSymbol {
	var syms []ExtractedSymbol
	extractTSNode(root, src, "", &syms)
	return syms
}

func extractTSNode(node *tree_sitter.Node, src []byte, parent string, syms *[]ExtractedSymbol) {
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		nodeType := child.Kind()

		switch nodeType {
		case "function_declaration":
			name := childFieldText(child, "name", src)
			params := childFieldText(child, "parameters", src)
			returnType := childFieldText(child, "return_type", src)
			sig := "function " + name + params
			if returnType != "" {
				sig += ": " + returnType
			}
			refs := extractCallRefs(child, src)
			*syms = append(*syms, ExtractedSymbol{
				Name:       name,
				Kind:       "function",
				Line:       int(child.StartPosition().Row) + 1,
				EndLine:    int(child.EndPosition().Row) + 1,
				Signature:  sig,
				ParentName: parent,
				References: refs,
			})

		case "class_declaration":
			name := childFieldText(child, "name", src)
			*syms = append(*syms, ExtractedSymbol{
				Name:      name,
				Kind:      "class",
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: "class " + name,
			})
			extractTSNode(child, src, name, syms)

		case "interface_declaration":
			name := childFieldText(child, "name", src)
			*syms = append(*syms, ExtractedSymbol{
				Name:      name,
				Kind:      "interface",
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: "interface " + name,
			})

		case "type_alias_declaration":
			name := childFieldText(child, "name", src)
			*syms = append(*syms, ExtractedSymbol{
				Name:      name,
				Kind:      "type",
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: "type " + name,
			})

		case "method_definition":
			name := childFieldText(child, "name", src)
			params := childFieldText(child, "parameters", src)
			sig := name + params
			refs := extractCallRefs(child, src)
			*syms = append(*syms, ExtractedSymbol{
				Name:       name,
				Kind:       "method",
				Line:       int(child.StartPosition().Row) + 1,
				EndLine:    int(child.EndPosition().Row) + 1,
				Signature:  sig,
				ParentName: parent,
				References: refs,
			})

		case "export_statement":
			extractTSNode(child, src, parent, syms)

		case "lexical_declaration":
			for j := uint(0); j < child.ChildCount(); j++ {
				decl := child.Child(j)
				if decl == nil || decl.Kind() != "variable_declarator" {
					continue
				}
				name := childFieldText(decl, "name", src)
				value := decl.ChildByFieldName("value")
				if value != nil && (value.Kind() == "arrow_function" || value.Kind() == "function") {
					params := childFieldText(value, "parameters", src)
					sig := "const " + name + " = " + params + " => ..."
					refs := extractCallRefs(value, src)
					*syms = append(*syms, ExtractedSymbol{
						Name:       name,
						Kind:       "function",
						Line:       int(child.StartPosition().Row) + 1,
						EndLine:    int(child.EndPosition().Row) + 1,
						Signature:  sig,
						ParentName: parent,
						References: refs,
					})
				}
			}

		default:
			extractTSNode(child, src, parent, syms)
		}
	}
}

func extractPython(root *tree_sitter.Node, src []byte) []ExtractedSymbol {
	var syms []ExtractedSymbol
	extractPyNode(root, src, "", &syms)
	return syms
}

func extractPyNode(node *tree_sitter.Node, src []byte, parent string, syms *[]ExtractedSymbol) {
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		nodeType := child.Kind()

		switch nodeType {
		case "function_definition":
			name := childFieldText(child, "name", src)
			params := childFieldText(child, "parameters", src)
			returnType := childFieldText(child, "return_type", src)
			sig := "def " + name + params
			if returnType != "" {
				sig += " -> " + returnType
			}
			kind := "function"
			if parent != "" {
				kind = "method"
			}
			refs := extractCallRefs(child, src)
			*syms = append(*syms, ExtractedSymbol{
				Name:       name,
				Kind:       kind,
				Line:       int(child.StartPosition().Row) + 1,
				EndLine:    int(child.EndPosition().Row) + 1,
				Signature:  sig,
				ParentName: parent,
				References: refs,
			})

		case "class_definition":
			name := childFieldText(child, "name", src)
			*syms = append(*syms, ExtractedSymbol{
				Name:      name,
				Kind:      "class",
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: "class " + name,
			})
			extractPyNode(child, src, name, syms)

		default:
			extractPyNode(child, src, parent, syms)
		}
	}
}

// helpers

func nodeText(n *tree_sitter.Node, src []byte) string {
	start := n.StartByte()
	end := n.EndByte()
	if start >= uint(len(src)) || end > uint(len(src)) {
		return ""
	}
	return string(src[start:end])
}

func childFieldText(n *tree_sitter.Node, field string, src []byte) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return nodeText(c, src)
}

func extractReceiverType(receiver string) string {
	receiver = strings.TrimPrefix(receiver, "(")
	receiver = strings.TrimSuffix(receiver, ")")
	parts := strings.Fields(receiver)
	if len(parts) >= 2 {
		return strings.TrimPrefix(parts[1], "*")
	}
	if len(parts) == 1 {
		return strings.TrimPrefix(parts[0], "*")
	}
	return ""
}

func extractCallRefs(node *tree_sitter.Node, src []byte) []string {
	var refs []string
	seen := make(map[string]bool)
	walkForCalls(node, src, seen, &refs)
	return refs
}

func walkForCalls(node *tree_sitter.Node, src []byte, seen map[string]bool, refs *[]string) {
	if node == nil {
		return
	}
	if node.Kind() == "call_expression" {
		fn := node.ChildByFieldName("function")
		if fn != nil {
			name := nodeText(fn, src)
			if idx := strings.LastIndex(name, "."); idx >= 0 {
				name = name[idx+1:]
			}
			if !seen[name] {
				seen[name] = true
				*refs = append(*refs, name)
			}
		}
	}
	for i := uint(0); i < node.ChildCount(); i++ {
		walkForCalls(node.Child(i), src, seen, refs)
	}
}
