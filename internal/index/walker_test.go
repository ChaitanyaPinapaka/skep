package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWalkDirFallback(t *testing.T) {
	// testdata/go-simple is not a git repo — should use WalkDir fallback
	root := filepath.Join(testdataDir(), "go-simple")
	results, err := WalkRepo(root)
	if err != nil {
		t.Fatalf("WalkRepo: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("got %d files, want 2", len(results))
	}

	for _, r := range results {
		if r.Language != "go" {
			t.Errorf("file %s: got language %q, want go", r.RelPath, r.Language)
		}
	}
}

func TestWalkRespectsIgnore(t *testing.T) {
	tmp := t.TempDir()

	// Create files
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(tmp, "node_modules", "dep.js"), []byte("module.exports = {}"), 0o644)
	os.MkdirAll(filepath.Join(tmp, "src"), 0o755)
	os.WriteFile(filepath.Join(tmp, "src", "app.ts"), []byte("export function main() {}"), 0o644)

	results, err := WalkRepo(tmp)
	if err != nil {
		t.Fatalf("WalkRepo: %v", err)
	}

	// Should find main.go and src/app.ts, NOT node_modules/dep.js
	paths := make(map[string]bool)
	for _, r := range results {
		paths[r.RelPath] = true
	}

	if !paths["main.go"] {
		t.Error("missing main.go")
	}
	if !paths[filepath.Join("src", "app.ts")] {
		t.Error("missing src/app.ts")
	}
	if paths[filepath.Join("node_modules", "dep.js")] {
		t.Error("node_modules/dep.js should be ignored")
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.ts", "typescript"},
		{"app.tsx", "typescript"},
		{"index.js", "javascript"},
		{"script.py", "python"},
		{"README.md", ""},
		{"data.json", ""},
		{"Makefile", ""},
		{"main.rs", "rust"},
		{"App.kt", "kotlin"},
	}

	for _, tt := range tests {
		got := detectLanguage(tt.path)
		if got != tt.want {
			t.Errorf("detectLanguage(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
