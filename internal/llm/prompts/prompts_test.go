package prompts

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

func TestClassifyGolden(t *testing.T) {
	data := ClassifyData{
		FileCount:   42,
		SymbolCount: 318,
		TopSymbols: []SymbolRef{
			{Name: "ServeHTTP", Kind: "method", FilePath: "internal/api/server.go", Line: 120, Signature: "func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)"},
			{Name: "AuthRepository", Kind: "class", FilePath: "app/data/AuthRepository.kt", Line: 14, Signature: "class AuthRepository(api: SessionApi)"},
		},
		Peers: []string{"backend", "mobile"},
		Task:  "switch mobile auth from the legacy session provider to backend /auth routes",
	}
	got, err := Classify(data)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	assertGolden(t, "classify.golden", got)
}

func TestExecuteGolden(t *testing.T) {
	data := ExecuteData{
		TaskID:          7,
		TaskName:        "add-health-check",
		TaskDescription: "add GET /health returning 200 OK",
		Plan:            "1. Add route\n2. Add handler\n3. Wire into router",
		FileCount:       12,
		SymbolCount:     84,
		Languages:       []string{"go"},
		FileTree:        []string{"cmd/app/main.go", "internal/api/server.go"},
		TopSymbols: []SymbolRef{
			{Name: "NewServer", Kind: "function", FilePath: "internal/api/server.go", Line: 30, Signature: "func NewServer() *Server"},
		},
		RelevantEdges: []string{"main() → NewServer()"},
		RecentCommits: []string{"abc123 initial commit"},
		TaskBranch:    "skep/task-7-add-health-check",
		BaseBranch:    "main",
		TestCmd:       "go test ./...",
	}
	got, err := Execute(data)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertGolden(t, "execute.golden", got)
}

func TestClassifyNoPeers(t *testing.T) {
	got, err := Classify(ClassifyData{FileCount: 1, SymbolCount: 1, Task: "fix typo"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// The rendered <peer_repos>...</peer_repos> block appears only when Peers is non-empty.
	// The instructions text *mentions* <peer_repos> as a reference — that's fine.
	// The block itself is always followed by a newline then content, so check for the opener
	// on its own line.
	if contains(got, "\n<peer_repos>\n") {
		t.Errorf("expected no rendered <peer_repos> block when Peers is empty, got:\n%s", got)
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch.\n--- want ---\n%s\n--- got ---\n%s", name, string(want), got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
