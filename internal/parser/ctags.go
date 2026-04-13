package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// parseCtags is a fallback symbol extractor that shells out to
// universal-ctags for languages we don't have a tree-sitter grammar for.
// Gives us ~90 additional languages (Ruby, Elixir, Lua, Zig, Haskell, Scala,
// Perl, OCaml, Nim, Crystal, etc.) at the cost of one subprocess per file.
//
// If ctags is not installed on PATH, returns an empty slice with no error
// so the indexer skips the file gracefully.
func parseCtags(path string) ([]ExtractedSymbol, error) {
	if _, err := exec.LookPath("ctags"); err != nil {
		return nil, nil
	}

	// --output-format=json  emits one JSON object per line, one per symbol
	// --fields=+nK          include line number (n) and long-form kind (K)
	// --sort=no             preserve file order (cheaper)
	cmd := exec.Command("ctags",
		"--output-format=json",
		"--fields=+nK",
		"--sort=no",
		"-f", "-",
		path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ctags %s: %w: %s", path, err, stderr.String())
	}

	var syms []ExtractedSymbol
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var tag struct {
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Line    int    `json:"line"`
			Pattern string `json:"pattern"`
			Scope   string `json:"scope"`
		}
		if err := json.Unmarshal([]byte(line), &tag); err != nil {
			continue
		}
		if tag.Name == "" {
			continue
		}
		// Pattern is the source line wrapped in /^...$/ — strip the markers
		// to get a usable signature.
		sig := strings.TrimPrefix(tag.Pattern, "/^")
		sig = strings.TrimSuffix(sig, "$/")
		sig = strings.TrimSpace(sig)

		syms = append(syms, ExtractedSymbol{
			Name:       tag.Name,
			Kind:       normalizeCtagsKind(tag.Kind),
			Line:       tag.Line,
			EndLine:    tag.Line, // ctags doesn't give end lines; good enough for navigation
			Signature:  sig,
			ParentName: tag.Scope,
		})
	}
	return syms, nil
}

// normalizeCtagsKind maps universal-ctags kind names onto the vocabulary the
// rest of skep uses. Keeps display consistent across tree-sitter and ctags.
func normalizeCtagsKind(k string) string {
	switch k {
	case "function", "func", "procedure":
		return "function"
	case "method":
		return "method"
	case "class":
		return "class"
	case "struct":
		return "struct"
	case "interface":
		return "interface"
	case "type", "typedef", "alias":
		return "type"
	case "enum":
		return "enum"
	case "module", "namespace", "package":
		return "module"
	case "variable", "constant", "field", "member", "property":
		return "variable"
	}
	return k
}
