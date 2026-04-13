package tasks

// Task status constants. Every place that reads or writes task.Status must
// reference one of these — never a string literal — so the compiler catches
// typos and refactors are mechanical.
const (
	StatusCreated              = "created"               // fresh row, not yet classified
	StatusClassified           = "classified"            // classifier completed but auto-execute not triggered
	StatusPending              = "pending"               // classified, awaiting human approval
	StatusPendingClarification = "pending_clarification" // classifier said the task is ambiguous; questions saved to .skep/clarify/<id>.md
	StatusApproved             = "approved"              // approved, waiting for the daemon's execute slot
	StatusQueued               = "queued"                // daemon picked it up, waiting behind another task
	StatusExecuting            = "executing"             // LLM is actively working on it
	StatusDone                 = "done"                  // commits in base branch, or read-only result file written
	StatusFailed               = "failed"                // execution errored; retryable
	StatusInterrupted          = "interrupted"           // exited mid-run or daemon crashed; retryable
	StatusRejected             = "rejected"              // human or classifier rejected; not retryable
)

// TerminalStatuses are the states a task can reach where no further
// automatic progress will happen without human intervention. Used by
// wait_remote_task and the stuck-executing sweep.
var TerminalStatuses = map[string]bool{
	StatusDone:        true,
	StatusFailed:      true,
	StatusInterrupted: true,
	StatusRejected:    true,
}

// RunnableStatuses are the states from which `skep task run <id>` is
// allowed to start (or resume) a task.
var RunnableStatuses = map[string]bool{
	StatusCreated:     true,
	StatusClassified:  true,
	StatusPending:     true,
	StatusApproved:    true,
	StatusQueued:      true,
	StatusFailed:      true,
	StatusInterrupted: true,
}

// NeedsClarification reports whether a status represents a task that
// the user must answer questions on before it can progress. Used by
// status/list commands to highlight work that needs attention.
func NeedsClarification(status string) bool {
	return status == StatusPendingClarification
}
