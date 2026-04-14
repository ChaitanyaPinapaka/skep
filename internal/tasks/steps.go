package tasks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// Step statuses. The set is intentionally small — the executor only
// needs to know "don't touch it", "working on it", "successful", or
// "gave up". Cross-ref Task.Status for the task-level rollup.
const (
	StepPending   = "pending"
	StepExecuting = "executing"
	StepDone      = "done"
	StepFailed    = "failed"
	StepSkipped   = "skipped"
)

// Step is one row in task_steps. The field set mirrors PlanStep at
// plan-generation time plus runtime state (status, result, session_id,
// retry_count, commit_sha, …) that accumulates as the executor works.
type Step struct {
	TaskID        int       `json:"task_id"`
	Seq           int       `json:"seq"`
	Verb          string    `json:"verb"`
	TargetFile    string    `json:"target_file,omitempty"`
	Symbols       []string  `json:"symbols,omitempty"`
	Acceptance    string    `json:"acceptance,omitempty"`
	DependsOn     []int     `json:"depends_on,omitempty"`
	Description   string    `json:"description,omitempty"`
	Status        string    `json:"status"`
	Result        string    `json:"result,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	ModelOverride string    `json:"model_override,omitempty"`
	TokensUsed    int       `json:"tokens_used,omitempty"`
	DurationMS    int       `json:"duration_ms,omitempty"`
	RetryCount    int       `json:"retry_count,omitempty"`
	CommitSHA     string    `json:"commit_sha,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// MaterializeSteps populates task_steps from a task's PlanStepsJSON. It
// is idempotent: if steps already exist for the task, it is a no-op.
// Returns the number of rows inserted (0 if already materialized or if
// the task has no plan).
//
// Called when a task transitions to `approved`. Separated from executor
// entry so `skep task show` can render per-step state immediately after
// approval, before the first shell-out.
func MaterializeSteps(store *index.Store, task *Task, modelByVerb map[string]string) (int, error) {
	if task.PlanStepsJSON == "" {
		return 0, nil
	}

	db := store.DB()

	// Idempotency check.
	var existing int
	if err := db.QueryRow(`SELECT count(*) FROM task_steps WHERE task_id=?`, task.ID).Scan(&existing); err != nil {
		return 0, fmt.Errorf("count existing steps: %w", err)
	}
	if existing > 0 {
		return 0, nil
	}

	var plan []PlanStep
	if err := json.Unmarshal([]byte(task.PlanStepsJSON), &plan); err != nil {
		return 0, fmt.Errorf("unmarshal plan_json: %w", err)
	}
	if len(plan) == 0 {
		return 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO task_steps (
			task_id, seq, verb, target_file, symbols_json, acceptance,
			depends_on_json, description, status, model_override
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	for i, ps := range plan {
		symbolsJSON, _ := json.Marshal(ps.Symbols)
		dependsJSON, _ := json.Marshal(ps.DependsOn)

		modelOverride := ""
		if modelByVerb != nil {
			modelOverride = modelByVerb[ps.Verb]
		}

		if _, err := stmt.Exec(
			task.ID, i+1, ps.Verb, ps.TargetFile, string(symbolsJSON),
			ps.Acceptance, string(dependsJSON), ps.Description,
			StepPending, modelOverride,
		); err != nil {
			return 0, fmt.Errorf("insert step %d: %w", i+1, err)
		}
		inserted++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit steps: %w", err)
	}
	return inserted, nil
}

// ListSteps returns all steps for a task in seq order.
func ListSteps(store *index.Store, taskID int) ([]*Step, error) {
	rows, err := store.DB().Query(`
		SELECT task_id, seq, verb, target_file, symbols_json, acceptance,
		       depends_on_json, description, status, result, session_id,
		       model_override, tokens_used, duration_ms, retry_count,
		       commit_sha, created_at, updated_at
		FROM task_steps WHERE task_id=? ORDER BY seq ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query steps: %w", err)
	}
	defer rows.Close()

	var out []*Step
	for rows.Next() {
		s, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// NextPendingStep returns the lowest-seq step that is not yet terminal
// (done/skipped). Used by the executor to pick the next step to run,
// and by the resume path to find where to continue after interruption.
// Returns (nil, nil) when every step is terminal — caller treats that
// as "task complete".
func NextPendingStep(store *index.Store, taskID int) (*Step, error) {
	row := store.DB().QueryRow(`
		SELECT task_id, seq, verb, target_file, symbols_json, acceptance,
		       depends_on_json, description, status, result, session_id,
		       model_override, tokens_used, duration_ms, retry_count,
		       commit_sha, created_at, updated_at
		FROM task_steps
		WHERE task_id=? AND status NOT IN (?, ?)
		ORDER BY seq ASC LIMIT 1`, taskID, StepDone, StepSkipped)

	s, err := scanStep(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// UpdateStep writes mutable fields back to task_steps. Immutable fields
// (task_id, seq, verb, target_file, symbols_json, acceptance,
// depends_on_json, description) are not touched — they are fixed at
// materialization time.
func UpdateStep(store *index.Store, s *Step) error {
	_, err := store.DB().Exec(`
		UPDATE task_steps SET
			status=?, result=?, session_id=?, model_override=?,
			tokens_used=?, duration_ms=?, retry_count=?, commit_sha=?,
			updated_at=CURRENT_TIMESTAMP
		WHERE task_id=? AND seq=?`,
		s.Status, s.Result, s.SessionID, s.ModelOverride,
		s.TokensUsed, s.DurationMS, s.RetryCount, s.CommitSHA,
		s.TaskID, s.Seq)
	if err != nil {
		return fmt.Errorf("update step: %w", err)
	}
	return nil
}

// StepSummary returns aggregate counts for a task's steps — used by
// `skep task list` and status views to show "3/5 steps done" without
// loading every row.
type StepSummary struct {
	Total     int
	Pending   int
	Executing int
	Done      int
	Failed    int
	Skipped   int
}

// SummarizeSteps returns a StepSummary for a task. Returns a zero-value
// summary (Total=0) when the task has no steps materialized.
func SummarizeSteps(store *index.Store, taskID int) (*StepSummary, error) {
	rows, err := store.DB().Query(`
		SELECT status, count(*) FROM task_steps WHERE task_id=? GROUP BY status`, taskID)
	if err != nil {
		return nil, fmt.Errorf("summarize steps: %w", err)
	}
	defer rows.Close()

	sum := &StepSummary{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		sum.Total += n
		switch status {
		case StepPending:
			sum.Pending = n
		case StepExecuting:
			sum.Executing = n
		case StepDone:
			sum.Done = n
		case StepFailed:
			sum.Failed = n
		case StepSkipped:
			sum.Skipped = n
		}
	}
	return sum, rows.Err()
}

func scanStep(row scannable) (*Step, error) {
	s := &Step{}
	var targetFile, symbolsJSON, acceptance, dependsJSON, description sql.NullString
	var result, sessionID, modelOverride, commitSHA sql.NullString
	var tokensUsed, durationMS, retryCount sql.NullInt64

	err := row.Scan(
		&s.TaskID, &s.Seq, &s.Verb, &targetFile, &symbolsJSON, &acceptance,
		&dependsJSON, &description, &s.Status, &result, &sessionID,
		&modelOverride, &tokensUsed, &durationMS, &retryCount,
		&commitSHA, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	s.TargetFile = targetFile.String
	s.Acceptance = acceptance.String
	s.Description = description.String
	s.Result = result.String
	s.SessionID = sessionID.String
	s.ModelOverride = modelOverride.String
	s.CommitSHA = commitSHA.String
	s.TokensUsed = int(tokensUsed.Int64)
	s.DurationMS = int(durationMS.Int64)
	s.RetryCount = int(retryCount.Int64)

	if symbolsJSON.Valid && symbolsJSON.String != "" && symbolsJSON.String != "null" {
		_ = json.Unmarshal([]byte(symbolsJSON.String), &s.Symbols)
	}
	if dependsJSON.Valid && dependsJSON.String != "" && dependsJSON.String != "null" {
		_ = json.Unmarshal([]byte(dependsJSON.String), &s.DependsOn)
	}

	return s, nil
}
