package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	skepDir := filepath.Join(dir, ".skep")
	store, err := index.OpenStore(filepath.Join(skepDir, "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &Server{root: dir, skepDir: skepDir, store: store}
}

func TestHandleToolsList(t *testing.T) {
	s := newTestServer(t)

	resp := s.handleToolsList(request{JSONRPC: "2.0", ID: float64(1), Method: "tools/list"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result not a map: %T", resp.Result)
	}
	toolsRaw, ok := result["tools"].([]toolDef)
	if !ok {
		t.Fatalf("tools field not []toolDef: %T", result["tools"])
	}
	if len(toolsRaw) == 0 {
		t.Fatal("expected at least one tool")
	}

	want := []string{
		"get_overview",
		"search_symbols",
		"get_call_graph",
		"get_file_context",
		"list_tasks",
		"create_task",
		"create_remote_task",
		"get_remote_task",
		"approve_remote_task",
		"wait_remote_task",
	}
	got := map[string]toolDef{}
	for _, td := range toolsRaw {
		if td.Name == "" {
			t.Errorf("tool with empty name: %+v", td)
		}
		if td.Description == "" {
			t.Errorf("tool %q has empty description", td.Name)
		}
		got[td.Name] = td
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestNextActionHint(t *testing.T) {
	cases := []struct {
		status   string
		mustHave string // substring sanity-check
	}{
		{tasks.StatusPending, "approve_remote_task"},
		{tasks.StatusApproved, "wait_remote_task"},
		{tasks.StatusExecuting, "wait_remote_task"},
		{tasks.StatusDone, "finished"},
		{tasks.StatusFailed, "failed"},
		{tasks.StatusInterrupted, "interrupted"},
		{tasks.StatusRejected, "rejected"},
		{"", "get_remote_task"}, // fallback
	}
	for _, tc := range cases {
		hint := nextActionHint(tc.status)
		if hint == "" {
			t.Errorf("status %q: empty hint", tc.status)
			continue
		}
		if !strings.Contains(hint, tc.mustHave) {
			t.Errorf("status %q: hint %q does not contain %q", tc.status, hint, tc.mustHave)
		}
	}
}
