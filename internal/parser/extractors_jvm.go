package parser

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// extractKotlin extracts the maximum set of navigable symbols from a Kotlin
// source file: classes, objects, interfaces, functions, methods, properties
// (top-level AND inside class bodies AND inside companion objects), enum
// entries, type aliases, and data-class constructor parameters (which
// Kotlin treats as properties).
//
// Grammar notes (from tree-sitter-kotlin, verified against a real Android
// file with the dumper test):
//   - Identifiers use kind "identifier" (not "simple_identifier")
//   - property_declaration nests its identifier one level deep inside a
//     variable_declaration child; our old shallow-walk missed all of them
//   - type_alias is a top-level kind with a direct identifier child
//   - enum values are "enum_entry" nodes inside an enum_class_body
//   - data-class constructor properties are "class_parameter" nodes
//     inside primary_constructor → class_parameters
//   - interfaces are represented as class_declaration with an
//     interface_modifier in the modifiers child; we surface them as
//     "interface" symbols rather than "class"
func extractKotlin(root *tree_sitter.Node, src []byte) []ExtractedSymbol {
	var syms []ExtractedSymbol
	extractKotlinNode(root, src, "", &syms)
	return syms
}

func extractKotlinNode(node *tree_sitter.Node, src []byte, parent string, syms *[]ExtractedSymbol) {
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		kind := child.Kind()

		switch kind {
		case "function_declaration":
			name := childFieldText(child, "name", src)
			if name == "" {
				name = firstDirectChildOfKind(child, "identifier", src)
			}
			sig := "fun " + name + childFieldText(child, "parameters", src)
			if rt := childFieldText(child, "return_type", src); rt != "" {
				sig += ": " + rt
			}
			symKind := "function"
			if parent != "" {
				symKind = "method"
			}
			refs := extractCallRefs(child, src)
			*syms = append(*syms, ExtractedSymbol{
				Name:       name,
				Kind:       symKind,
				Line:       int(child.StartPosition().Row) + 1,
				EndLine:    int(child.EndPosition().Row) + 1,
				Signature:  sig,
				ParentName: parent,
				References: refs,
			})
			// Local functions inside a function body are rare and noisy;
			// don't recurse into function bodies here.

		case "class_declaration":
			name := childFieldText(child, "name", src)
			if name == "" {
				name = firstDirectChildOfKind(child, "identifier", src)
			}
			// Kotlin tree-sitter collapses class + interface + enum + sealed
			// + data into class_declaration with a distinguishing modifier.
			// Detect the actual kind by inspecting the modifiers child.
			k := "class"
			switch classFlavor(child, src) {
			case "interface":
				k = "interface"
			case "enum":
				k = "enum"
			case "data":
				k = "class" // data classes are still classes; flavor only affects sig
			case "sealed":
				k = "class"
			}
			*syms = append(*syms, ExtractedSymbol{
				Name:      name,
				Kind:      k,
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: k + " " + name,
			})
			// Pull data-class constructor properties out of the primary
			// constructor: `data class User(val id: String, val email: String)`
			// contributes id + email as navigable properties.
			extractKotlinCtorProperties(child, src, name, syms)
			extractKotlinNode(child, src, name, syms)

		case "object_declaration":
			name := childFieldText(child, "name", src)
			if name == "" {
				name = firstDirectChildOfKind(child, "identifier", src)
			}
			*syms = append(*syms, ExtractedSymbol{
				Name:      name,
				Kind:      "object",
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: "object " + name,
			})
			extractKotlinNode(child, src, name, syms)

		case "companion_object":
			// Emit a synthetic "Companion" symbol scoped under the enclosing
			// class, then recurse into it so the properties and functions
			// inside the companion object pick up `<class>.Companion` as
			// their parent for navigation.
			companionName := parent + ".Companion"
			if parent == "" {
				companionName = "Companion"
			}
			*syms = append(*syms, ExtractedSymbol{
				Name:      companionName,
				Kind:      "object",
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: "companion object",
			})
			extractKotlinNode(child, src, companionName, syms)

		case "property_declaration":
			// Kotlin nests the property name inside a variable_declaration
			// child. Our old shallow walk missed it entirely. Walk down one
			// level and grab the first `identifier` inside the variable
			// declaration.
			name := kotlinPropertyName(child, src)
			if name != "" {
				*syms = append(*syms, ExtractedSymbol{
					Name:       name,
					Kind:       "property",
					Line:       int(child.StartPosition().Row) + 1,
					EndLine:    int(child.EndPosition().Row) + 1,
					Signature:  truncateLine(nodeText(child, src), 120),
					ParentName: parent,
				})
			}

		case "type_alias":
			name := firstDirectChildOfKind(child, "identifier", src)
			if name != "" {
				*syms = append(*syms, ExtractedSymbol{
					Name:      name,
					Kind:      "type",
					Line:      int(child.StartPosition().Row) + 1,
					EndLine:   int(child.EndPosition().Row) + 1,
					Signature: "typealias " + name,
				})
			}

		case "enum_entry":
			name := firstDirectChildOfKind(child, "identifier", src)
			if name != "" {
				*syms = append(*syms, ExtractedSymbol{
					Name:       name,
					Kind:       "enum_value",
					Line:       int(child.StartPosition().Row) + 1,
					EndLine:    int(child.EndPosition().Row) + 1,
					Signature:  name,
					ParentName: parent,
				})
			}

		default:
			// Intermediate nodes (class_body, enum_class_body, modifiers, etc.)
			// — recurse through them so child symbols are still extracted.
			extractKotlinNode(child, src, parent, syms)
		}
	}
}

// kotlinPropertyName drills into a property_declaration to find the name.
// Kotlin grammar structure:
//
//	property_declaration
//	  modifiers (optional)
//	  variable_declaration
//	    identifier "name"      ← we want this
//	    user_type (optional)
//	  property_delegate / expression / literal (optional initializer)
func kotlinPropertyName(node *tree_sitter.Node, src []byte) string {
	// Look for a variable_declaration direct child first (the common case).
	for i := uint(0); i < node.ChildCount(); i++ {
		c := node.Child(i)
		if c != nil && c.Kind() == "variable_declaration" {
			if id := firstDirectChildOfKind(c, "identifier", src); id != "" {
				return id
			}
		}
	}
	// Fallback: scan direct children for any identifier (rare edge case).
	return firstDirectChildOfKind(node, "identifier", src)
}

// extractKotlinCtorProperties finds `val`/`var` parameters in a class's
// primary_constructor and emits them as property symbols. Required for
// data classes to have navigable fields.
func extractKotlinCtorProperties(classNode *tree_sitter.Node, src []byte, parent string, syms *[]ExtractedSymbol) {
	// Find primary_constructor direct child.
	var ctor *tree_sitter.Node
	for i := uint(0); i < classNode.ChildCount(); i++ {
		c := classNode.Child(i)
		if c != nil && c.Kind() == "primary_constructor" {
			ctor = c
			break
		}
	}
	if ctor == nil {
		return
	}
	// primary_constructor → class_parameters → class_parameter*
	for i := uint(0); i < ctor.ChildCount(); i++ {
		cp := ctor.Child(i)
		if cp == nil || cp.Kind() != "class_parameters" {
			continue
		}
		for j := uint(0); j < cp.ChildCount(); j++ {
			param := cp.Child(j)
			if param == nil || param.Kind() != "class_parameter" {
				continue
			}
			name := firstDirectChildOfKind(param, "identifier", src)
			if name == "" {
				continue
			}
			*syms = append(*syms, ExtractedSymbol{
				Name:       name,
				Kind:       "property",
				Line:       int(param.StartPosition().Row) + 1,
				EndLine:    int(param.EndPosition().Row) + 1,
				Signature:  truncateLine(nodeText(param, src), 120),
				ParentName: parent,
			})
		}
	}
}

// classFlavor inspects a class_declaration's modifiers child for
// interface/enum/data/sealed markers. Returns "" if no special flavor.
func classFlavor(classNode *tree_sitter.Node, src []byte) string {
	for i := uint(0); i < classNode.ChildCount(); i++ {
		c := classNode.Child(i)
		if c == nil || c.Kind() != "modifiers" {
			continue
		}
		// Walk the modifiers children. Kotlin grammar emits specific
		// modifier kinds like "class_modifier" / "interface_modifier";
		// we also check raw text for defensive matching.
		for j := uint(0); j < c.ChildCount(); j++ {
			m := c.Child(j)
			if m == nil {
				continue
			}
			text := strings.TrimSpace(nodeText(m, src))
			switch text {
			case "interface":
				return "interface"
			case "enum":
				return "enum"
			case "data":
				return "data"
			case "sealed":
				return "sealed"
			}
		}
	}
	return ""
}

// truncateLine cuts a signature at the first newline and caps its length
// so multi-line property initializers don't bloat the index.
func truncateLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return strings.TrimSpace(s)
}

// firstDirectChildOfKind scans the immediate children of a node and
// returns the text of the first child matching the given kind. Used
// everywhere we expect an identifier one level deep.
func firstDirectChildOfKind(node *tree_sitter.Node, kind string, src []byte) string {
	for i := uint(0); i < node.ChildCount(); i++ {
		c := node.Child(i)
		if c != nil && c.Kind() == kind {
			return nodeText(c, src)
		}
	}
	return ""
}

// extractJava extracts classes, interfaces, methods, fields from Java.
func extractJava(root *tree_sitter.Node, src []byte) []ExtractedSymbol {
	var syms []ExtractedSymbol
	extractJavaNode(root, src, "", &syms)
	return syms
}

func extractJavaNode(node *tree_sitter.Node, src []byte, parent string, syms *[]ExtractedSymbol) {
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		kind := child.Kind()

		switch kind {
		case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration":
			name := childFieldText(child, "name", src)
			k := "class"
			switch kind {
			case "interface_declaration":
				k = "interface"
			case "enum_declaration":
				k = "enum"
			case "record_declaration":
				k = "record"
			}
			*syms = append(*syms, ExtractedSymbol{
				Name:      name,
				Kind:      k,
				Line:      int(child.StartPosition().Row) + 1,
				EndLine:   int(child.EndPosition().Row) + 1,
				Signature: k + " " + name,
			})
			extractJavaNode(child, src, name, syms)

		case "method_declaration", "constructor_declaration":
			name := childFieldText(child, "name", src)
			params := childFieldText(child, "parameters", src)
			returnType := childFieldText(child, "type", src)
			sig := name + params
			if returnType != "" {
				sig = returnType + " " + sig
			}
			symKind := "method"
			if kind == "constructor_declaration" {
				symKind = "constructor"
			}
			refs := extractCallRefs(child, src)
			*syms = append(*syms, ExtractedSymbol{
				Name:       name,
				Kind:       symKind,
				Line:       int(child.StartPosition().Row) + 1,
				EndLine:    int(child.EndPosition().Row) + 1,
				Signature:  sig,
				ParentName: parent,
				References: refs,
			})

		case "field_declaration":
			// Field declarations can contain multiple variables
			name := findIdentifierChild(child, src)
			if name != "" {
				*syms = append(*syms, ExtractedSymbol{
					Name:       name,
					Kind:       "field",
					Line:       int(child.StartPosition().Row) + 1,
					EndLine:    int(child.EndPosition().Row) + 1,
					Signature:  nodeText(child, src),
					ParentName: parent,
				})
			}

		default:
			extractJavaNode(child, src, parent, syms)
		}
	}
}

// findIdentifierChild walks a node looking for the first "identifier" or "simple_identifier" child.
func findIdentifierChild(node *tree_sitter.Node, src []byte) string {
	for i := uint(0); i < node.ChildCount(); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		k := c.Kind()
		if k == "identifier" || k == "simple_identifier" || k == "type_identifier" {
			return nodeText(c, src)
		}
	}
	return ""
}
