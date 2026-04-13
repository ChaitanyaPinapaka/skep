package parser

import (
	"fmt"

	tree_sitter_kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

var languages = map[string]*tree_sitter.Language{
	"go":         tree_sitter.NewLanguage(tree_sitter_go.Language()),
	"typescript": tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
	"javascript": tree_sitter.NewLanguage(tree_sitter_javascript.Language()),
	"python":     tree_sitter.NewLanguage(tree_sitter_python.Language()),
	"kotlin":     tree_sitter.NewLanguage(tree_sitter_kotlin.Language()),
	"java":       tree_sitter.NewLanguage(tree_sitter_java.Language()),
}

// GetLanguage returns a tree-sitter Language for the given language name.
func GetLanguage(lang string) (*tree_sitter.Language, error) {
	l, ok := languages[lang]
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}
	return l, nil
}

// ctagsFallbackLanguages lists languages that we don't have a tree-sitter
// grammar for but which universal-ctags handles well. Supported() returns
// true for these so the indexer attempts extraction; if ctags isn't on PATH
// ParseFile returns an empty symbol slice and the file is indexed with
// zero symbols (still useful — file exists, counted in totals).
var ctagsFallbackLanguages = map[string]bool{
	"rust":     true,
	"ruby":     true,
	"elixir":   true,
	"lua":      true,
	"zig":      true,
	"haskell":  true,
	"scala":    true,
	"perl":     true,
	"ocaml":    true,
	"nim":      true,
	"crystal":  true,
	"c":        true,
	"cpp":      true,
	"csharp":   true,
	"php":      true,
	"swift":    true,
	"r":        true,
	"shell":    true,
	"bash":     true,
	"make":     true,
	"cmake":    true,
	"protobuf": true,
	"sql":      true,
}

// Supported returns true if the language has any parser (tree-sitter,
// declarative, or universal-ctags fallback).
func Supported(lang string) bool {
	if _, ok := languages[lang]; ok {
		return true
	}
	switch lang {
	case "yaml", "terraform", "dockerfile", "json":
		return true
	}
	if ctagsFallbackLanguages[lang] {
		return true
	}
	return false
}
