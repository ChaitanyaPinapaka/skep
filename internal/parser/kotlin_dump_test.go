package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// TestDumpKotlinAST is a one-off AST dumper to see what tree-sitter-kotlin
// actually emits for a realistic Android-style Kotlin file. Not a real test
// — prints the tree to stdout when run with `go test -run TestDumpKotlinAST
// -v ./internal/parser/`. Kept as a test so it can be executed cheaply via
// the standard test machinery without adding a new cmd target.
func TestDumpKotlinAST(t *testing.T) {
	path := filepath.Join(testdataDir(), "kotlin-simple", "MainActivity.kt")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	lang, err := GetLanguage("kotlin")
	if err != nil {
		t.Fatalf("get lang: %v", err)
	}

	p := tree_sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(lang); err != nil {
		t.Fatalf("set lang: %v", err)
	}

	tree := p.Parse(src, nil)
	defer tree.Close()

	var out strings.Builder
	dumpNode(&out, tree.RootNode(), src, 0)
	t.Logf("\n%s", out.String())

	// Also run the real extractor and log what it found
	syms := extractKotlin(tree.RootNode(), src)
	t.Logf("\nextractKotlin found %d symbols:", len(syms))
	for _, s := range syms {
		t.Logf("  %-18s %-10s L%d  parent=%q  sig=%q", s.Name, s.Kind, s.Line, s.ParentName, s.Signature)
	}
}

func dumpNode(b *strings.Builder, n *tree_sitter.Node, src []byte, depth int) {
	if n == nil {
		return
	}
	// Only print "interesting" nodes — skip operators, keywords, punctuation
	kind := n.Kind()
	skip := kind == "=" || kind == "{" || kind == "}" || kind == "(" || kind == ")" ||
		kind == ";" || kind == "," || kind == ":" || kind == "." || kind == "<" || kind == ">" ||
		kind == "fun" || kind == "val" || kind == "var" || kind == "class" || kind == "object" ||
		kind == "interface" || kind == "override" || kind == "private" || kind == "public" ||
		kind == "companion" || kind == "data" || kind == "sealed" || kind == "enum" ||
		kind == "return" || kind == "package" || kind == "import" || kind == "const" ||
		kind == "typealias" || kind == "by" || kind == "lazy" || kind == "as" || kind == "in" ||
		kind == "super" || kind == "this" || kind == "null" || kind == "true" || kind == "false" ||
		kind == "->" || kind == "?." || kind == "?" || kind == "!!"

	if !skip {
		indent := strings.Repeat("  ", depth)
		preview := ""
		if n.ChildCount() == 0 {
			// Leaf — show its text
			text := nodeText(n, src)
			if len(text) > 40 {
				text = text[:40] + "…"
			}
			preview = fmt.Sprintf(" %q", text)
		}
		fmt.Fprintf(b, "%s%s%s\n", indent, kind, preview)
	}

	for i := uint(0); i < n.ChildCount(); i++ {
		dumpNode(b, n.Child(i), src, depth+1)
	}
}
