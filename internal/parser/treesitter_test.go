package parser

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

func TestParseGoFile(t *testing.T) {
	path := filepath.Join(testdataDir(), "go-simple", "main.go")
	syms, err := ParseFile(path, "go")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	want := map[string]string{
		"Server":    "struct",
		"NewServer": "function",
		"Start":     "method",
		"Handler":   "interface",
		"main":      "function",
	}

	found := make(map[string]string)
	for _, s := range syms {
		found[s.Name] = s.Kind
	}

	for name, kind := range want {
		got, ok := found[name]
		if !ok {
			t.Errorf("missing symbol: %s", name)
			continue
		}
		if got != kind {
			t.Errorf("symbol %s: got kind %q, want %q", name, got, kind)
		}
	}
}

func TestParseGoMethodReceiver(t *testing.T) {
	path := filepath.Join(testdataDir(), "go-simple", "main.go")
	syms, err := ParseFile(path, "go")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	for _, s := range syms {
		if s.Name == "Start" {
			if s.Kind != "method" {
				t.Errorf("Start: got kind %q, want method", s.Kind)
			}
			if s.ParentName != "Server" {
				t.Errorf("Start: got parent %q, want Server", s.ParentName)
			}
			if s.Signature == "" {
				t.Error("Start: empty signature")
			}
			return
		}
	}
	t.Error("Start method not found")
}

func TestParseGoAuth(t *testing.T) {
	path := filepath.Join(testdataDir(), "go-simple", "auth.go")
	syms, err := ParseFile(path, "go")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	names := make(map[string]bool)
	for _, s := range syms {
		names[s.Name] = true
	}

	if !names["AuthMiddleware"] {
		t.Error("missing AuthMiddleware")
	}
	if !names["ValidateToken"] {
		t.Error("missing ValidateToken")
	}
}

func TestParseGoCallRefs(t *testing.T) {
	path := filepath.Join(testdataDir(), "go-simple", "main.go")
	syms, err := ParseFile(path, "go")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	for _, s := range syms {
		if s.Name == "main" {
			if len(s.References) == 0 {
				t.Error("main: expected call references (NewServer, Start)")
			}
			refSet := make(map[string]bool)
			for _, r := range s.References {
				refSet[r] = true
			}
			if !refSet["NewServer"] {
				t.Error("main: missing reference to NewServer")
			}
			return
		}
	}
	t.Error("main function not found")
}

func TestParseTypeScript(t *testing.T) {
	path := filepath.Join(testdataDir(), "ts-simple", "app.ts")
	syms, err := ParseFile(path, "typescript")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	want := map[string]string{
		"User":        "interface",
		"UserRole":    "type",
		"UserService": "class",
		"getUser":     "method",
		"deleteUser":  "method",
		"createApp":   "function",
		"fetchUsers":  "function",
	}

	found := make(map[string]string)
	for _, s := range syms {
		found[s.Name] = s.Kind
	}

	for name, kind := range want {
		got, ok := found[name]
		if !ok {
			t.Errorf("missing symbol: %s", name)
			continue
		}
		if got != kind {
			t.Errorf("symbol %s: got kind %q, want %q", name, got, kind)
		}
	}
}

func TestParseTSMethodParent(t *testing.T) {
	path := filepath.Join(testdataDir(), "ts-simple", "app.ts")
	syms, err := ParseFile(path, "typescript")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	for _, s := range syms {
		if s.Name == "getUser" {
			if s.ParentName != "UserService" {
				t.Errorf("getUser: got parent %q, want UserService", s.ParentName)
			}
			return
		}
	}
	t.Error("getUser method not found")
}

func TestUnsupportedLanguage(t *testing.T) {
	// A language genuinely unknown to both tree-sitter and ctags fallback
	// list should return no error and no symbols — the indexer treats it as
	// "file detected, nothing to parse." We write a real empty file so the
	// ctags fallback path doesn't fail on ENOENT.
	dir := t.TempDir()
	p := dir + "/fake.xyz"
	if err := os.WriteFile(p, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	syms, err := ParseFile(p, "xyz-not-a-language")
	if err != nil {
		t.Errorf("unexpected error for unknown language: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("expected 0 symbols for unknown language, got %d", len(syms))
	}
}
