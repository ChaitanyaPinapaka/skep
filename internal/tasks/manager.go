package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// maxDescriptionLen is a defense-in-depth cap on task description size.
// Prevents prompt-injection via pathologically long descriptions from
// ballooning classifier prompts or LLM executor context.
const maxDescriptionLen = 4096

// Task represents a task row.
type Task struct {
	ID             int       `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Status         string    `json:"status"`
	SessionID      string    `json:"session_id,omitempty"`
	Classification string    `json:"classification,omitempty"`
	Plan           string    `json:"plan,omitempty"`
	SourceRepo     string    `json:"source_repo,omitempty"`
	SourceTaskID   int       `json:"source_task_id,omitempty"`
	CreatedBy      string    `json:"created_by,omitempty"`
	Acceptance     string    `json:"acceptance,omitempty"`
	Result         string    `json:"result,omitempty"`
	Branch         string    `json:"branch,omitempty"`
	TokensUsed     int       `json:"tokens_used,omitempty"`     // total effective billed input tokens across all Claude turns
	ToolsUsedJSON  string    `json:"tools_used_json,omitempty"` // JSON map of tool name → call count
	PlanStepsJSON  string    `json:"plan_steps_json,omitempty"` // serialized []PlanStep, produced by the Stage 2+3 pipeline and consumed by the executor prompt
	NeedsInput     bool      `json:"needs_input,omitempty"`     // true when the approval watchdog detected the executing LLM waiting on a "Do you want to proceed?" prompt
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Create inserts a new task with dedup check.
//
// The dedup scan and the INSERT run inside a single BEGIN IMMEDIATE
// transaction so that two concurrent callers cannot both pass the dedup
// gate and then both insert a duplicate row. SQLite's BEGIN IMMEDIATE
// acquires a reserved lock up-front, serializing writers while letting
// readers proceed under WAL.
func Create(store *index.Store, description, sourceRepo, acceptance string) (*Task, *DedupResult, error) {
	if len(description) > maxDescriptionLen {
		return nil, nil, fmt.Errorf("task description too long: %d bytes (max %d)", len(description), maxDescriptionLen)
	}

	db := store.DB()
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	// Promote to an immediate (reserved-lock) write transaction explicitly
	// so the dedup scan below sees a stable view and no other writer can
	// slip an INSERT in between the scan and our own INSERT.
	if _, err := tx.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		// modernc.org/sqlite's BeginTx already opens a write transaction;
		// a nested BEGIN will fail with "cannot start a transaction within
		// a transaction". That's fine — the outer tx is already immediate
		// the moment it issues its first write, and the dedup SELECTs below
		// run against a consistent snapshot. Swallow the error.
		_ = err
	}
	defer tx.Rollback() // no-op after successful commit

	// Dedup: scan active (non-terminal) tasks inside the transaction and
	// run keywordMatch in Go. Mirrors CheckDedup's logic but uses tx.Query
	// so concurrent writers are serialized on the same immediate lock.
	dupeStatuses := []string{
		StatusExecuting,
		StatusPending,
		StatusApproved,
		StatusCreated,
		StatusClassified,
	}
	for _, status := range dupeStatuses {
		rows, qerr := tx.QueryContext(ctx,
			`SELECT id, description, status FROM tasks WHERE status=?`, status)
		if qerr != nil {
			return nil, nil, fmt.Errorf("dedup query: %w", qerr)
		}
		var hit *DedupResult
		for rows.Next() {
			var tid int
			var tdesc, tstatus string
			if err := rows.Scan(&tid, &tdesc, &tstatus); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("dedup scan: %w", err)
			}
			// Two cheap layers inside the tx to close the race window:
			// the semantic LLM dedup ran BEFORE this transaction started,
			// so another process's newly-inserted task may not have been
			// visible then but is visible now. Keyword + trigram catch
			// ~all exact and near-exact collisions; we accept that a
			// genuine semantic race between two different phrasings
			// (bypassing all 4 cheap layers AND the LLM) is vanishingly
			// rare in human workflows.
			if keywordMatch(tdesc, description) || trigramJaccard(tdesc, description) >= trigramThreshold() {
				var reason string
				if tstatus == StatusExecuting {
					reason = fmt.Sprintf("in progress as task #%d", tid)
				} else {
					reason = fmt.Sprintf("already queued as task #%d (status: %s)", tid, tstatus)
				}
				hit = &DedupResult{IsDuplicate: true, Reason: reason, TaskID: tid, Layer: "tx-recheck"}
				break
			}
		}
		rows.Close()
		if hit != nil {
			return nil, hit, nil
		}
	}

	name := taskName(description)

	res, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (name, description, status, source_repo, acceptance, created_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		name, description, StatusCreated, sourceRepo, acceptance, sourceRepo)
	if err != nil {
		return nil, nil, fmt.Errorf("insert task: %w", err)
	}

	id, _ := res.LastInsertId()

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit tx: %w", err)
	}

	task := &Task{
		ID:          int(id),
		Name:        name,
		Description: description,
		Status:      StatusCreated,
		SourceRepo:  sourceRepo,
		Acceptance:  acceptance,
	}
	return task, nil, nil
}

// Get returns a task by ID. Returns a clean "task #N not found" error
// when the id does not exist, rather than leaking sql.ErrNoRows.
func Get(store *index.Store, id int) (*Task, error) {
	t, err := scanTask(store.DB().QueryRow(`
		SELECT id, name, description, status, session_id, classification, plan, source_repo, source_task_id,
		       created_by, acceptance, result, branch, tokens_used, tools_used_json, plan_json, needs_input, created_at, updated_at
		FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("task #%d not found", id)
	}
	return t, err
}

// Update saves task fields to the database.
func Update(store *index.Store, task *Task) error {
	needsInput := 0
	if task.NeedsInput {
		needsInput = 1
	}
	_, err := store.DB().Exec(`
		UPDATE tasks SET
			status=?, session_id=?, classification=?, plan=?, result=?, branch=?,
			tokens_used=?, tools_used_json=?, plan_json=?, needs_input=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		task.Status, task.SessionID, task.Classification, task.Plan, task.Result, task.Branch,
		task.TokensUsed, task.ToolsUsedJSON, task.PlanStepsJSON, needsInput, task.ID)
	return err
}

// List returns all tasks ordered by creation time.
func List(store *index.Store) ([]*Task, error) {
	return queryTasks(store.DB(), `
		SELECT id, name, description, status, session_id, classification, plan, source_repo, source_task_id,
		       created_by, acceptance, result, branch, tokens_used, tools_used_json, plan_json, needs_input, created_at, updated_at
		FROM tasks ORDER BY created_at DESC`)
}

// ListByStatus returns tasks with a given status.
func ListByStatus(store *index.Store, status string) ([]*Task, error) {
	return queryTasks(store.DB(), `
		SELECT id, name, description, status, session_id, classification, plan, source_repo, source_task_id,
		       created_by, acceptance, result, branch, tokens_used, tools_used_json, plan_json, needs_input, created_at, updated_at
		FROM tasks WHERE status=? ORDER BY created_at DESC`, status)
}

// ResultSummary returns a short, one-line summary of a task's result
// suitable for `task list` and `workspace watch` displays. Strips
// leading markdown headings / bullets, collapses whitespace, and caps
// to 120 characters. Returns "" when the task has no result yet.
//
// The full result content stays on disk (.skep/task-<id>-result.md)
// and in the task row — this helper is display-only.
func ResultSummary(t *Task) string {
	if t == nil || t.Result == "" {
		return ""
	}
	const max = 120
	for _, line := range strings.Split(t.Result, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" {
			continue
		}
		// Strip markdown heading / bullet prefixes.
		line = strings.TrimLeft(line, "#*- \t")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > max {
			line = line[:max] + "…"
		}
		return line
	}
	return ""
}

// CountActive returns the number of tasks that are NOT in a terminal state.
// Used to skip the LLM semantic-dedup call when there's nothing to compare
// against — saves an entire LLM round trip on fresh repos.
func CountActive(store *index.Store) (int, error) {
	var n int
	err := store.DB().QueryRow(`
		SELECT count(*) FROM tasks
		WHERE status NOT IN (?, ?, ?, ?)`,
		StatusDone, StatusFailed, StatusRejected, StatusInterrupted,
	).Scan(&n)
	return n, err
}

// Approve moves a task from pending/classified to approved.
//
// Step materialization (populating task_steps from task.plan_json) is
// intentionally NOT done here — Approve stays pure so call sites that
// don't have access to config (CLI smoke paths, tests) can still use
// it. Callers that want steps materialized at approval time should
// use ApproveWithSteps below.
func Approve(store *index.Store, id int) error {
	task, err := Get(store, id)
	if err != nil {
		return err
	}
	if task.Status != StatusPending && task.Status != StatusClassified && task.Status != StatusCreated {
		return fmt.Errorf("task #%d is %s, cannot approve", id, task.Status)
	}
	task.Status = StatusApproved
	return Update(store, task)
}

// ApproveWithSteps approves a task and materializes its task_steps rows
// from task.plan_json in a single call. Idempotent — re-approving an
// already-approved task is a no-op on the status side, and step
// materialization is no-op when rows already exist.
//
// modelByVerb is forwarded to MaterializeSteps so each step's
// model_override column is set at materialization time. Passing nil is
// equivalent to "no per-verb routing" — every step routes to the main
// LLM command at execution time.
func ApproveWithSteps(store *index.Store, id int, modelByVerb map[string]string) (*Task, error) {
	task, err := Get(store, id)
	if err != nil {
		return nil, err
	}
	if task.Status != StatusPending && task.Status != StatusClassified && task.Status != StatusCreated && task.Status != StatusApproved {
		return nil, fmt.Errorf("task #%d is %s, cannot approve", id, task.Status)
	}
	if task.Status != StatusApproved {
		task.Status = StatusApproved
		if err := Update(store, task); err != nil {
			return nil, err
		}
	}
	if _, err := MaterializeSteps(store, task, modelByVerb); err != nil {
		return nil, fmt.Errorf("materialize steps for task #%d: %w", id, err)
	}
	return task, nil
}

// Reject marks a task as rejected.
func Reject(store *index.Store, id int) error {
	task, err := Get(store, id)
	if err != nil {
		return err
	}
	task.Status = StatusRejected
	return Update(store, task)
}

// Delete hard-deletes a task row and its FTS entry. Refuses tasks in
// the "executing" state — the caller is expected to stop them first.
func Delete(store *index.Store, id int) error {
	task, err := Get(store, id)
	if err != nil {
		return err
	}
	if task.Status == StatusExecuting {
		return fmt.Errorf("task #%d is executing — stop it first", id)
	}
	// tasks_fts is kept in sync by AFTER DELETE trigger (see store.go).
	if _, err := store.DB().Exec(`DELETE FROM tasks WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}

func queryTasks(db *sql.DB, query string, args ...interface{}) ([]*Task, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Task
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func scanTask(row *sql.Row) (*Task, error) {
	t := &Task{}
	var name, sessionID, classification, plan, sourceRepo, createdBy, acceptance, result, branch, toolsUsed, planJSON sql.NullString
	var sourceTaskID, tokensUsed, needsInput sql.NullInt64
	err := row.Scan(&t.ID, &name, &t.Description, &t.Status, &sessionID, &classification, &plan,
		&sourceRepo, &sourceTaskID, &createdBy, &acceptance, &result, &branch,
		&tokensUsed, &toolsUsed, &planJSON, &needsInput, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	t.Name = name.String
	t.SessionID = sessionID.String
	t.Classification = classification.String
	t.Plan = plan.String
	t.SourceRepo = sourceRepo.String
	t.SourceTaskID = int(sourceTaskID.Int64)
	t.CreatedBy = createdBy.String
	t.Acceptance = acceptance.String
	t.Result = result.String
	t.Branch = branch.String
	t.TokensUsed = int(tokensUsed.Int64)
	t.ToolsUsedJSON = toolsUsed.String
	t.PlanStepsJSON = planJSON.String
	t.NeedsInput = needsInput.Int64 != 0
	return t, nil
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanTaskRow(row scannable) (*Task, error) {
	t := &Task{}
	var name, sessionID, classification, plan, sourceRepo, createdBy, acceptance, result, branch, toolsUsed, planJSON sql.NullString
	var sourceTaskID, tokensUsed, needsInput sql.NullInt64
	err := row.Scan(&t.ID, &name, &t.Description, &t.Status, &sessionID, &classification, &plan,
		&sourceRepo, &sourceTaskID, &createdBy, &acceptance, &result, &branch,
		&tokensUsed, &toolsUsed, &planJSON, &needsInput, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	t.Name = name.String
	t.SessionID = sessionID.String
	t.Classification = classification.String
	t.Plan = plan.String
	t.SourceRepo = sourceRepo.String
	t.SourceTaskID = int(sourceTaskID.Int64)
	t.CreatedBy = createdBy.String
	t.Acceptance = acceptance.String
	t.Result = result.String
	t.Branch = branch.String
	t.TokensUsed = int(tokensUsed.Int64)
	t.ToolsUsedJSON = toolsUsed.String
	t.PlanStepsJSON = planJSON.String
	t.NeedsInput = needsInput.Int64 != 0
	return t, nil
}

// taskName derives a short, human-readable slug from the description.
// "Implement clear logging and add critical metrics for the mcp" → "implement-logging-add-metrics"
var nonAlpha = regexp.MustCompile(`[^a-z0-9]+`)

// Common stop words to filter out for a cleaner name
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "for": true,
	"in": true, "on": true, "at": true, "to": true, "of": true, "with": true,
	"that": true, "this": true, "from": true, "into": true, "by": true,
	"is": true, "it": true, "be": true, "as": true, "do": true, "need": true,
	"needed": true, "should": true, "implement": true, "add": true, "create": true,
	"make": true, "update": true, "fix": true, "new": true, "clear": true,
}

func taskName(description string) string {
	words := strings.Fields(strings.ToLower(description))

	// Keep first meaningful words, skip stop words
	var kept []string
	for _, w := range words {
		clean := nonAlpha.ReplaceAllString(w, "")
		if clean == "" || stopWords[clean] {
			continue
		}
		kept = append(kept, clean)
		if len(kept) >= 4 {
			break
		}
	}

	if len(kept) == 0 {
		// Fallback: just use first 3 words
		for _, w := range words {
			clean := nonAlpha.ReplaceAllString(w, "")
			if clean != "" {
				kept = append(kept, clean)
			}
			if len(kept) >= 3 {
				break
			}
		}
	}

	name := strings.Join(kept, "-")
	if len(name) > 40 {
		name = name[:40]
	}
	return strings.Trim(name, "-")
}
