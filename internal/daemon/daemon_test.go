package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/logfile"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
)

// newTestDaemon builds a Daemon with a real (empty) SQLite store and an
// in-memory terminal — enough to exercise dispatch() and sweepStuckExecuting()
// without any real socket, tmux, or LLM shell-out.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	root := t.TempDir()
	skepDir := filepath.Join(root, ".skep")
	store, err := index.OpenStore(filepath.Join(skepDir, "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	d := &Daemon{
		Root:    root,
		SkepDir: skepDir,
		Store:   store,
		Config:  &config.Config{},
		Term:    NewTerminal(1, "", nil),
		Log:     logfile.IndexLog(skepDir),
		ctx:     ctx,
		cancel:  cancel,
	}
	return d
}

func TestDispatch_Status(t *testing.T) {
	d := newTestDaemon(t)
	resp := d.dispatch(Request{Cmd: "status"})
	if !resp.OK {
		t.Fatalf("status: expected OK, got error=%q", resp.Error)
	}
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("status: data not a map: %T", resp.Data)
	}
	for _, key := range []string{"files", "symbols", "root", "pid", "active_tasks", "queued_tasks"} {
		if _, ok := data[key]; !ok {
			t.Errorf("status: missing key %q", key)
		}
	}
}

func TestDispatch_ListTasks_Empty(t *testing.T) {
	d := newTestDaemon(t)
	resp := d.dispatch(Request{Cmd: "list_tasks"})
	if !resp.OK {
		t.Fatalf("list_tasks: %s", resp.Error)
	}
	list, ok := resp.Data.([]*tasks.Task)
	if !ok {
		t.Fatalf("list_tasks: data type %T", resp.Data)
	}
	if len(list) != 0 {
		t.Errorf("expected empty task list, got %d", len(list))
	}
}

func TestDispatch_CreateTask(t *testing.T) {
	d := newTestDaemon(t)
	resp := d.dispatch(Request{Cmd: "create_task", Description: "add a new /export endpoint"})
	if !resp.OK {
		t.Fatalf("create_task: %s", resp.Error)
	}
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("create_task: data %T", resp.Data)
	}
	if _, ok := data["task_id"]; !ok {
		t.Errorf("create_task: no task_id in response data: %+v", data)
	}
}

func TestDispatch_ApproveTask_Missing(t *testing.T) {
	d := newTestDaemon(t)
	// No task_id at all.
	resp := d.dispatch(Request{Cmd: "approve_task"})
	if resp.Error == "" {
		t.Errorf("approve_task without id: expected error, got OK=%v data=%+v", resp.OK, resp.Data)
	}
	// Non-existent task id.
	resp = d.dispatch(Request{Cmd: "approve_task", TaskID: 99999})
	if resp.Error == "" {
		t.Errorf("approve_task on missing row: expected error, got OK=%v data=%+v", resp.OK, resp.Data)
	}
}

func TestDispatch_UnknownCommand(t *testing.T) {
	d := newTestDaemon(t)
	resp := d.dispatch(Request{Cmd: "fly_to_the_moon"})
	if resp.Error == "" {
		t.Errorf("unknown command: expected error")
	}
}

func TestDispatch_StopCancelsContext(t *testing.T) {
	d := newTestDaemon(t)
	resp := d.dispatch(Request{Cmd: "stop"})
	if !resp.OK {
		t.Fatalf("stop: %s", resp.Error)
	}
	select {
	case <-d.ctx.Done():
		// good — cancel fires ~100ms after stop
	case <-time.After(2 * time.Second):
		t.Error("stop: context not cancelled within 2s")
	}
}

func TestSweepStuckExecuting(t *testing.T) {
	d := newTestDaemon(t)

	// Create a task then flip it to executing directly via tasks.Update.
	task, _, err := tasks.Create(d.Store, "stuck task from prior crash", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	task.Status = tasks.StatusExecuting
	if err := tasks.Update(d.Store, task); err != nil {
		t.Fatalf("update: %v", err)
	}

	d.sweepStuckExecuting()

	reloaded, err := tasks.Get(d.Store, task.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != tasks.StatusInterrupted {
		t.Errorf("status = %q, want %q", reloaded.Status, tasks.StatusInterrupted)
	}
}
