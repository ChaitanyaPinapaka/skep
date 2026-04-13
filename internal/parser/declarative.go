package parser

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseDeclarative is a simple line-based extractor for config formats
// (YAML, Terraform, Dockerfile, JSON). No tree-sitter — these are declarative
// and a line scan is sufficient to extract top-level structure.
func parseDeclarative(path, lang string) ([]ExtractedSymbol, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	switch lang {
	case "yaml":
		return parseYAML(f)
	case "terraform":
		return parseTerraform(f)
	case "dockerfile":
		return parseDockerfile(f)
	case "json":
		return parseJSON(f)
	}
	return nil, nil
}

// parseYAML extracts top-level keys and Kubernetes-style "kind: Name" pairs.
func parseYAML(f *os.File) ([]ExtractedSymbol, error) {
	var syms []ExtractedSymbol
	scanner := bufio.NewScanner(f)
	line := 0
	var currentKind string
	for scanner.Scan() {
		line++
		text := scanner.Text()
		trimmed := strings.TrimLeft(text, " \t")

		// Skip comments and empty lines
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(text) - len(trimmed)

		// Top-level keys: "foo:" at indent 0
		if indent == 0 && strings.HasSuffix(strings.TrimRight(trimmed, " "), ":") {
			name := strings.TrimSuffix(strings.TrimSpace(trimmed), ":")
			if isValidIdent(name) {
				syms = append(syms, ExtractedSymbol{
					Name: name, Kind: "yaml-key",
					Line: line, EndLine: line,
					Signature: text,
				})
			}
			continue
		}

		// Kubernetes / Compose: kind: Foo → remember, then name: bar pairs with it
		if strings.HasPrefix(trimmed, "kind:") {
			currentKind = strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
		}
		if strings.HasPrefix(trimmed, "name:") && currentKind != "" {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
			if name != "" {
				syms = append(syms, ExtractedSymbol{
					Name: name, Kind: strings.ToLower(currentKind),
					Line: line, EndLine: line,
					Signature: fmt.Sprintf("%s: %s", currentKind, name),
				})
				currentKind = ""
			}
		}
	}
	return syms, scanner.Err()
}

// parseTerraform extracts resource, module, variable, output, data blocks.
// Example: `resource "aws_s3_bucket" "my_bucket" {` → name: "aws_s3_bucket.my_bucket", kind: resource
func parseTerraform(f *os.File) ([]ExtractedSymbol, error) {
	var syms []ExtractedSymbol
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "//") {
			continue
		}

		for _, kind := range []string{"resource", "module", "variable", "output", "data", "provider", "locals", "terraform"} {
			if !strings.HasPrefix(text, kind+" ") && !strings.HasPrefix(text, kind+"{") {
				continue
			}
			// Extract quoted names
			parts := extractQuoted(text)
			var name string
			switch kind {
			case "resource", "data":
				if len(parts) >= 2 {
					name = parts[0] + "." + parts[1]
				}
			case "module", "variable", "output", "provider":
				if len(parts) >= 1 {
					name = parts[0]
				}
			case "locals", "terraform":
				name = kind
			}
			if name != "" {
				syms = append(syms, ExtractedSymbol{
					Name: name, Kind: kind,
					Line: line, EndLine: line,
					Signature: scanner.Text(),
				})
			}
			break
		}
	}
	return syms, scanner.Err()
}

// parseDockerfile extracts FROM, ENV, EXPOSE, CMD, ENTRYPOINT, ARG, WORKDIR, RUN (first word).
func parseDockerfile(f *os.File) ([]ExtractedSymbol, error) {
	var syms []ExtractedSymbol
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		instr := strings.ToUpper(fields[0])
		switch instr {
		case "FROM", "EXPOSE", "ENV", "CMD", "ENTRYPOINT", "ARG", "WORKDIR", "USER", "LABEL":
			value := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			syms = append(syms, ExtractedSymbol{
				Name: instr, Kind: "dockerfile-" + strings.ToLower(instr),
				Line: line, EndLine: line,
				Signature:  text,
				DocComment: value,
			})
		}
	}
	return syms, scanner.Err()
}

// parseJSON extracts top-level keys from a JSON file (package.json, tsconfig, etc.)
func parseJSON(f *os.File) ([]ExtractedSymbol, error) {
	var syms []ExtractedSymbol
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	depth := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()

		// Track brace depth so we only extract top-level keys
		for _, c := range text {
			switch c {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}

		// At depth 1 (inside the root object), look for "key":
		if depth != 1 {
			continue
		}
		trimmed := strings.TrimLeft(text, " \t")
		if !strings.HasPrefix(trimmed, `"`) {
			continue
		}
		// Extract the key
		end := strings.Index(trimmed[1:], `"`)
		if end < 0 {
			continue
		}
		key := trimmed[1 : 1+end]
		if !isValidIdent(key) {
			continue
		}
		syms = append(syms, ExtractedSymbol{
			Name: key, Kind: "json-key",
			Line: line, EndLine: line,
			Signature: strings.TrimSpace(text),
		})
	}
	return syms, scanner.Err()
}

// extractQuoted pulls out all "..."-quoted strings from a line in order.
func extractQuoted(s string) []string {
	var out []string
	var buf []byte
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			if inQuote {
				out = append(out, string(buf))
				buf = nil
			}
			inQuote = !inQuote
			continue
		}
		if inQuote {
			buf = append(buf, c)
		}
	}
	return out
}

// isValidIdent returns true if s is a reasonable identifier-like name
// (letters, digits, hyphens, underscores, dots).
func isValidIdent(s string) bool {
	if len(s) == 0 || len(s) > 100 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == '/') {
			return false
		}
	}
	return true
}
