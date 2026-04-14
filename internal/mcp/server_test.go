package mcp

import (
	"encoding/json"
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

// seedTask inserts a task via the normal Create path and returns it.
// Used by the local task-verb tool tests so each one starts from a
// fresh, real task row.
func seedTask(t *testing.T, s *Server, description string) *tasks.Task {
	t.Helper()
	task, dedup, err := tasks.Create(s.store, description, "", "")
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if dedup != nil && dedup.IsDuplicate {
		t.Fatalf("seed task unexpectedly flagged as duplicate: %s", dedup.Reason)
	}
	return task
}

// decodeText pulls the JSON payload out of a textResponse and unmarshals
// it into out. Fails the test on any decode error or if the response is
// flagged as an MCP tool error (IsError=true on the toolResult).
func decodeText(t *testing.T, resp response, out interface{}) {
	t.Helper()
	tr, ok := resp.Result.(toolResult)
	if !ok {
		t.Fatalf("result not a toolResult: %T", resp.Result)
	}
	if tr.IsError {
		t.Fatalf("tool returned error: %s", tr.Content[0].Text)
	}
	if len(tr.Content) == 0 || tr.Content[0].Type != "text" {
		t.Fatalf("expected text content, got %+v", tr.Content)
	}
	if err := json.Unmarshal([]byte(tr.Content[0].Text), out); err != nil {
		t.Fatalf("unmarshal text: %v\nbody=%s", err, tr.Content[0].Text)
	}
}

// isToolError reports whether an MCP tool response is the error variant.
// Tool errors come back as a toolResult with IsError=true, not as the
// JSON-RPC response.Error field.
func isToolError(resp response) bool {
	tr, ok := resp.Result.(toolResult)
	if !ok {
		return false
	}
	return tr.IsError
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
		"show_task",
		"approve_task",
		"reject_task",
		"clarify_task",
		"delete_task",
		"dedup_task",
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

// TestToolShowTask seeds a task and verifies show_task returns the
// expected fields and refuses an invalid id.
func TestToolShowTask(t *testing.T) {
	s := newTestServer(t)
	task := seedTask(t, s, "add a /health endpoint that returns 200")

	args, _ := json.Marshal(map[string]interface{}{"task_id": task.ID})
	resp := s.toolShowTask(float64(1), args)
	var got map[string]interface{}
	decodeText(t, resp, &got)

	if got["task_id"].(float64) != float64(task.ID) {
		t.Errorf("task_id = %v, want %d", got["task_id"], task.ID)
	}
	if got["description"].(string) != task.Description {
		t.Errorf("description mismatch: %v", got["description"])
	}
	if got["status"].(string) != tasks.StatusCreated {
		t.Errorf("status = %v, want %s", got["status"], tasks.StatusCreated)
	}

	// Missing task_id is a usage error, not a panic.
	bad := s.toolShowTask(float64(2), json.RawMessage(`{}`))
	if !isToolError(bad) {
		t.Error("expected error for missing task_id, got success")
	}

	// Nonexistent task surfaces the underlying lookup error.
	args2, _ := json.Marshal(map[string]interface{}{"task_id": 99999})
	missing := s.toolShowTask(float64(3), args2)
	if !isToolError(missing) {
		t.Error("expected error for nonexistent task, got success")
	}
}

// TestToolApproveTask verifies a task moves to approved and that
// approving an invalid id returns an error.
func TestToolApproveTask(t *testing.T) {
	s := newTestServer(t)
	task := seedTask(t, s, "rename Logger to AuditLogger")

	args, _ := json.Marshal(map[string]interface{}{"task_id": task.ID})
	resp := s.toolApproveTask(float64(1), args)
	var got map[string]interface{}
	decodeText(t, resp, &got)

	if got["status"].(string) != tasks.StatusApproved {
		t.Errorf("status = %v, want %s", got["status"], tasks.StatusApproved)
	}

	// Verify persistence — a fresh Get should agree.
	fresh, err := tasks.Get(s.store, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if fresh.Status != tasks.StatusApproved {
		t.Errorf("persisted status = %s, want %s", fresh.Status, tasks.StatusApproved)
	}

	bad := s.toolApproveTask(float64(2), json.RawMessage(`{"task_id": 0}`))
	if !isToolError(bad) {
		t.Error("expected error for task_id=0")
	}
}

// TestToolRejectTask verifies a task moves to rejected and persistence.
func TestToolRejectTask(t *testing.T) {
	s := newTestServer(t)
	task := seedTask(t, s, "delete the entire src directory")

	args, _ := json.Marshal(map[string]interface{}{"task_id": task.ID})
	resp := s.toolRejectTask(float64(1), args)
	var got map[string]interface{}
	decodeText(t, resp, &got)

	if got["status"].(string) != tasks.StatusRejected {
		t.Errorf("status = %v, want %s", got["status"], tasks.StatusRejected)
	}

	fresh, _ := tasks.Get(s.store, task.ID)
	if fresh.Status != tasks.StatusRejected {
		t.Errorf("persisted status = %s, want %s", fresh.Status, tasks.StatusRejected)
	}
}

// TestToolDeleteTask verifies delete removes the row and refuses
// executing tasks. The double-guard (mcp + tasks.Delete) is intentional:
// MCP rejects with a friendly message before the SQL call.
func TestToolDeleteTask(t *testing.T) {
	s := newTestServer(t)
	task := seedTask(t, s, "add unit tests for the helper package")

	args, _ := json.Marshal(map[string]interface{}{"task_id": task.ID})
	resp := s.toolDeleteTask(float64(1), args)
	var got map[string]interface{}
	decodeText(t, resp, &got)

	if got["deleted"] != true {
		t.Errorf("deleted = %v, want true", got["deleted"])
	}

	// Row should be gone.
	if _, err := tasks.Get(s.store, task.ID); err == nil {
		t.Error("expected task to be gone after delete")
	}

	// Refuses executing tasks.
	exec := seedTask(t, s, "long-running migration")
	exec.Status = tasks.StatusExecuting
	if err := tasks.Update(s.store, exec); err != nil {
		t.Fatalf("set executing: %v", err)
	}
	execArgs, _ := json.Marshal(map[string]interface{}{"task_id": exec.ID})
	badExec := s.toolDeleteTask(float64(2), execArgs)
	if !isToolError(badExec) {
		t.Error("expected error deleting executing task")
	}
}

// TestToolClarifyTask covers the validation paths only — the happy path
// runs the full classify+plan pipeline which shells out to a real LLM
// CLI and lives in the integration package, not here.
func TestToolClarifyTask(t *testing.T) {
	s := newTestServer(t)
	task := seedTask(t, s, "make the api faster")

	// Status is StatusCreated (not pending_clarification) — should refuse.
	args, _ := json.Marshal(map[string]interface{}{
		"task_id": task.ID,
		"answers": []map[string]string{{"question": "which endpoint?", "answer": "POST /export"}},
	})
	resp := s.toolClarifyTask(float64(1), args)
	if !isToolError(resp) {
		t.Error("expected error: task is not in pending_clarification")
	}

	// Empty answers list is a usage error.
	emptyArgs, _ := json.Marshal(map[string]interface{}{
		"task_id": task.ID,
		"answers": []map[string]string{},
	})
	emptyResp := s.toolClarifyTask(float64(2), emptyArgs)
	if !isToolError(emptyResp) {
		t.Error("expected error for empty answers list")
	}

	// Missing task_id.
	missingResp := s.toolClarifyTask(float64(3), json.RawMessage(`{"answers":[{"question":"q","answer":"a"}]}`))
	if !isToolError(missingResp) {
		t.Error("expected error for missing task_id")
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
