package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSessionFile writes a JSONL session file under the fake HOME dir at
// ~/.claude/projects/<encoded-abs-path>/<sessionID>.jsonl and returns the
// session ID used.
func writeSessionFile(t *testing.T, home, repoRoot, sessionID, body string) {
	t.Helper()
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	encoded := strings.ReplaceAll(abs, string(filepath.Separator), "-")
	dir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestReadSessionUsage_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	repoRoot := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Three assistant turns with usage + tool_use blocks, plus a non-assistant
	// line that must be ignored.
	lines := []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"assistant","timestamp":"2026-04-12T00:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"search_symbols"}],"usage":{"input_tokens":1000,"output_tokens":100,"cache_creation_input_tokens":500,"cache_read_input_tokens":2000}}}`,
		`{"type":"assistant","timestamp":"2026-04-12T00:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"search_symbols"},{"type":"tool_use","name":"get_overview"}],"usage":{"input_tokens":2000,"output_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":4000}}}`,
		`{"type":"assistant","timestamp":"2026-04-12T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":500,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":1000}}}`,
		``,
		`this is garbage and should be skipped`,
	}
	body := strings.Join(lines, "\n") + "\n"
	writeSessionFile(t, home, repoRoot, "deadbeef-uuid", body)

	u := ReadSessionUsage(repoRoot, "deadbeef-uuid")
	if u == nil {
		t.Fatal("expected non-nil SessionUsage")
	}
	if u.Turns != 3 {
		t.Errorf("Turns = %d, want 3", u.Turns)
	}
	if u.InputTokens != 3500 {
		t.Errorf("InputTokens = %d, want 3500", u.InputTokens)
	}
	if u.OutputTokens != 350 {
		t.Errorf("OutputTokens = %d, want 350", u.OutputTokens)
	}
	if u.CacheCreationInputTokens != 500 {
		t.Errorf("CacheCreationInputTokens = %d, want 500", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 7000 {
		t.Errorf("CacheReadInputTokens = %d, want 7000", u.CacheReadInputTokens)
	}
	// EffectiveBilledInput = input + cache_read/10 = 3500 + 700 = 4200
	if u.EffectiveBilledInput != 4200 {
		t.Errorf("EffectiveBilledInput = %d, want 4200", u.EffectiveBilledInput)
	}
	if u.ToolCalls != 3 {
		t.Errorf("ToolCalls = %d, want 3", u.ToolCalls)
	}
	if got := u.ToolCallsByName["search_symbols"]; got != 2 {
		t.Errorf("ToolCallsByName[search_symbols] = %d, want 2", got)
	}
	if got := u.ToolCallsByName["get_overview"]; got != 1 {
		t.Errorf("ToolCallsByName[get_overview] = %d, want 1", got)
	}
}

func TestReadSessionUsage_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if u := ReadSessionUsage(filepath.Join(tmp, "repo"), "does-not-exist"); u != nil {
		t.Errorf("expected nil for missing file, got %+v", u)
	}
	// Empty sessionID should short-circuit to nil.
	if u := ReadSessionUsage(filepath.Join(tmp, "repo"), ""); u != nil {
		t.Errorf("expected nil for empty session id, got %+v", u)
	}
}
