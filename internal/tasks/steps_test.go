package tasks

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// newTestStore opens a fresh index.Store in a temp dir. Mirrors the
// pattern used by internal/index/indexer_test.go.
func newTestStore(t *testing.T) *index.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := index.OpenStore(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// insertTestTask writes a minimal task row and returns its Task struct
// with the inserted id. Avoids going through Create so tests don't
// depend on the dedup / classification pipeline.
func insertTestTask(t *testing.T, store *index.Store, planJSON string) *Task {
	t.Helper()
	res, err := store.DB().Exec(`
		INSERT INTO tasks (name, description, status, plan_json)
		VALUES (?, ?, ?, ?)`,
		"test", "test task", StatusApproved, planJSON)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	id, _ := res.LastInsertId()
	return &Task{
		ID:            int(id),
		Name:          "test",
		Description:   "test task",
		Status:        StatusApproved,
		PlanStepsJSON: planJSON,
	}
}

func TestMaterializeSteps(t *testing.T) {
	store := newTestStore(t)

	plan := []PlanStep{
		{Verb: "modify", TargetFile: "a.go", Symbols: []string{"Foo", "Bar"}, Acceptance: "Foo no longer panics", Description: "Patch Foo"},
		{Verb: "test", TargetFile: "a_test.go", Acceptance: "go test passes", Description: "Add test"},
		{Verb: "add", TargetFile: "b.go", DependsOn: []int{0}, Description: "New helper"},
	}
	raw, _ := json.Marshal(plan)
	task := insertTestTask(t, store, string(raw))

	modelByVerb := map[string]string{"test": "haiku"}
	n, err := MaterializeSteps(store, task, modelByVerb)
	if err != nil {
		t.Fatalf("MaterializeSteps: %v", err)
	}
	if n != 3 {
		t.Fatalf("MaterializeSteps inserted %d steps, want 3", n)
	}

	// Idempotency: second call is a no-op.
	n2, err := MaterializeSteps(store, task, modelByVerb)
	if err != nil {
		t.Fatalf("MaterializeSteps (2nd call): %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second MaterializeSteps inserted %d steps, want 0", n2)
	}

	steps, err := ListSteps(store, task.ID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("ListSteps returned %d steps, want 3", len(steps))
	}

	// Seq order preserved.
	for i, s := range steps {
		if s.Seq != i+1 {
			t.Errorf("step %d: Seq = %d, want %d", i, s.Seq, i+1)
		}
	}

	// Verb and target preserved.
	if steps[0].Verb != "modify" || steps[0].TargetFile != "a.go" {
		t.Errorf("step 1: verb=%s target=%s, want modify/a.go", steps[0].Verb, steps[0].TargetFile)
	}

	// Symbols + depends_on round-tripped through JSON.
	if got := steps[0].Symbols; len(got) != 2 || got[0] != "Foo" || got[1] != "Bar" {
		t.Errorf("step 1 symbols = %v, want [Foo Bar]", got)
	}
	if got := steps[2].DependsOn; len(got) != 1 || got[0] != 0 {
		t.Errorf("step 3 depends_on = %v, want [0]", got)
	}

	// ModelOverride applied per verb.
	if steps[1].ModelOverride != "haiku" {
		t.Errorf("step 2 (verb=test) model_override = %q, want haiku", steps[1].ModelOverride)
	}
	if steps[0].ModelOverride != "" {
		t.Errorf("step 1 (verb=modify) model_override = %q, want empty", steps[0].ModelOverride)
	}

	// All steps start pending.
	for _, s := range steps {
		if s.Status != StepPending {
			t.Errorf("step %d: status = %s, want pending", s.Seq, s.Status)
		}
	}
}

func TestMaterializeSteps_NoPlan(t *testing.T) {
	store := newTestStore(t)
	task := insertTestTask(t, store, "")

	n, err := MaterializeSteps(store, task, nil)
	if err != nil {
		t.Fatalf("MaterializeSteps: %v", err)
	}
	if n != 0 {
		t.Errorf("MaterializeSteps with empty plan inserted %d, want 0", n)
	}
}

func TestNextPendingStep(t *testing.T) {
	store := newTestStore(t)
	plan := []PlanStep{
		{Verb: "modify", Description: "first"},
		{Verb: "test", Description: "second"},
		{Verb: "verify", Description: "third"},
	}
	raw, _ := json.Marshal(plan)
	task := insertTestTask(t, store, string(raw))
	if _, err := MaterializeSteps(store, task, nil); err != nil {
		t.Fatalf("MaterializeSteps: %v", err)
	}

	// Initially: next is seq=1.
	s, err := NextPendingStep(store, task.ID)
	if err != nil || s == nil {
		t.Fatalf("NextPendingStep: s=%v err=%v", s, err)
	}
	if s.Seq != 1 {
		t.Errorf("NextPendingStep = seq %d, want 1", s.Seq)
	}

	// Mark seq=1 done, expect seq=2.
	s.Status = StepDone
	if err := UpdateStep(store, s); err != nil {
		t.Fatalf("UpdateStep: %v", err)
	}
	s, err = NextPendingStep(store, task.ID)
	if err != nil || s == nil {
		t.Fatalf("NextPendingStep after done: s=%v err=%v", s, err)
	}
	if s.Seq != 2 {
		t.Errorf("NextPendingStep after done = seq %d, want 2", s.Seq)
	}

	// Mark seq=2 skipped, expect seq=3.
	s.Status = StepSkipped
	if err := UpdateStep(store, s); err != nil {
		t.Fatalf("UpdateStep skipped: %v", err)
	}
	s, err = NextPendingStep(store, task.ID)
	if err != nil || s == nil {
		t.Fatalf("NextPendingStep after skip: s=%v err=%v", s, err)
	}
	if s.Seq != 3 {
		t.Errorf("NextPendingStep after skip = seq %d, want 3", s.Seq)
	}

	// Mark seq=3 done, expect nil (task complete).
	s.Status = StepDone
	if err := UpdateStep(store, s); err != nil {
		t.Fatalf("UpdateStep done final: %v", err)
	}
	s, err = NextPendingStep(store, task.ID)
	if err != nil {
		t.Fatalf("NextPendingStep final: %v", err)
	}
	if s != nil {
		t.Errorf("NextPendingStep after all terminal = %+v, want nil", s)
	}
}

func TestSummarizeSteps(t *testing.T) {
	store := newTestStore(t)
	plan := []PlanStep{
		{Verb: "a", Description: "1"},
		{Verb: "b", Description: "2"},
		{Verb: "c", Description: "3"},
		{Verb: "d", Description: "4"},
	}
	raw, _ := json.Marshal(plan)
	task := insertTestTask(t, store, string(raw))
	if _, err := MaterializeSteps(store, task, nil); err != nil {
		t.Fatalf("MaterializeSteps: %v", err)
	}

	// Advance states: done, executing, failed, pending.
	steps, _ := ListSteps(store, task.ID)
	steps[0].Status = StepDone
	steps[1].Status = StepExecuting
	steps[2].Status = StepFailed
	for _, s := range steps[:3] {
		if err := UpdateStep(store, s); err != nil {
			t.Fatalf("UpdateStep: %v", err)
		}
	}

	sum, err := SummarizeSteps(store, task.ID)
	if err != nil {
		t.Fatalf("SummarizeSteps: %v", err)
	}
	if sum.Total != 4 {
		t.Errorf("Total = %d, want 4", sum.Total)
	}
	if sum.Done != 1 || sum.Executing != 1 || sum.Failed != 1 || sum.Pending != 1 {
		t.Errorf("summary = %+v, want done=1 executing=1 failed=1 pending=1", sum)
	}
}

func TestApproveWithSteps(t *testing.T) {
	store := newTestStore(t)
	plan := []PlanStep{{Verb: "modify", Description: "test"}}
	raw, _ := json.Marshal(plan)

	// Insert directly in 'classified' state to test the transition.
	res, err := store.DB().Exec(`
		INSERT INTO tasks (name, description, status, plan_json)
		VALUES (?, ?, ?, ?)`,
		"test", "test", StatusClassified, string(raw))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id, _ := res.LastInsertId()

	task, err := ApproveWithSteps(store, int(id), nil)
	if err != nil {
		t.Fatalf("ApproveWithSteps: %v", err)
	}
	if task.Status != StatusApproved {
		t.Errorf("Status = %s, want approved", task.Status)
	}

	steps, _ := ListSteps(store, int(id))
	if len(steps) != 1 {
		t.Fatalf("ListSteps = %d, want 1", len(steps))
	}

	// Re-approving an already-approved task should be a no-op, not an error.
	if _, err := ApproveWithSteps(store, int(id), nil); err != nil {
		t.Errorf("re-approve returned error: %v", err)
	}
	steps2, _ := ListSteps(store, int(id))
	if len(steps2) != 1 {
		t.Errorf("steps after re-approve = %d, want 1 (idempotent)", len(steps2))
	}
}

func TestInjectModelFlag(t *testing.T) {
	// This test lives alongside the tasks package tests to keep executor
	// internals accessible, but targets injectModelFlag in step_executor.go
	// via the tasks package boundary. Skipped for now — the helper is
	// covered implicitly by executor smoke tests when they exist.
	t.Skip("covered by future executor smoke test")
}
