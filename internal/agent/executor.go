package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/llm"
	"github.com/ChaitanyaPinapaka/skep/internal/logfile"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
	"github.com/ChaitanyaPinapaka/skep/internal/trust"
)

// Execute runs a task interactively:
//  1. Build structured prompt with codebase context
//  2. Launch Claude TUI (new session or --resume if session exists)
//  3. Capture session_id from stream for future resume
//  4. After exit: check for commits, update task status
func Execute(root, skepDir string, store *index.Store, cfg *config.Config, task *tasks.Task) error {
	baseBranch, err := GitCurrentBranch(root)
	if err != nil {
		baseBranch = "main"
	}

	branchName := task.Name
	if branchName == "" {
		branchName = slugify(task.Description)
	}
	taskBranch := fmt.Sprintf("skep/task-%d-%s", task.ID, branchName)

	// Branch-level log + crash recovery
	blog := logfile.BranchLog(skepDir, taskBranch)
	defer logfile.RecoverWith(blog, fmt.Sprintf("task #%d execution", task.ID))()

	blog.Log("task #%d started: %s", task.ID, task.Description)
	blog.Log("base branch: %s, task branch: %s", baseBranch, taskBranch)

	task.Status = tasks.StatusExecuting
	task.Branch = taskBranch
	tasks.Update(store, task)

	// Resume existing session or start new one
	var llmErr error
	if task.SessionID != "" && isValidSessionID(task.SessionID) {
		fmt.Fprintf(os.Stderr, "skep: resuming task #%d (session %s)\n", task.ID, task.SessionID[:12])
		sessionID, err := llm.ShellOutResume(root, task.SessionID)
		if sessionID != "" {
			task.SessionID = sessionID
		}
		llmErr = err
	} else {
		if task.SessionID != "" {
			fmt.Fprintf(os.Stderr, "skep: clearing invalid session ID, starting fresh\n")
			task.SessionID = ""
		}
		taskInfo := llm.TaskInfo{
			ID: task.ID, Name: task.Name,
			Description: task.Description, Plan: task.Plan, PlanStepsJSON: task.PlanStepsJSON, Acceptance: task.Acceptance,
		}
		promptCtx := llm.BuildPromptContext(store, root, taskBranch, baseBranch, cfg.TestCmd, task.Description)

		prompt := llm.BuildExecutionPrompt(taskInfo, promptCtx)
		systemPrompt := llm.BuildSystemPrompt(taskInfo)

		fmt.Fprintf(os.Stderr, "skep: task #%d → %s\n", task.ID, taskBranch)

		sessionID, err := llm.ShellOutInteractive(root, cfg.LLMCmd(), prompt, systemPrompt)
		if sessionID != "" {
			task.SessionID = sessionID
		}
		llmErr = err
	}

	// ALWAYS re-index after LLM exits — whether done, interrupted, or failed.
	// Claude may have edited files even if it didn't commit.
	fmt.Fprintf(os.Stderr, "skep: re-indexing...\n")
	index.EnsureFresh(root, skepDir)

	// Check what happened
	hasCommits := gitHasCommits(root, baseBranch, taskBranch)
	mergedToBase := GitAllCommitsInBase(root, baseBranch, taskBranch)

	// Diff branch vs main
	var diff *index.IndexDiff
	branchStore, indexErr := index.IndexBranch(root, skepDir, taskBranch)
	if indexErr == nil && branchStore != nil {
		diff, _ = index.DiffBranch(store, branchStore)
		branchStore.Close()
		if diff != nil && !diff.IsEmpty() {
			task.Result = formatDiff(diff)
		}
	}

	// Read the result file if Claude wrote one. This is how read-only/review tasks
	// return their output, and how cross-repo callers retrieve summaries.
	resultFile := filepath.Join(root, ".skep", fmt.Sprintf("task-%d-result.md", task.ID))
	hasResultFile := false
	if content, err := os.ReadFile(resultFile); err == nil && len(content) > 0 {
		hasResultFile = true
		if task.Result != "" {
			task.Result = string(content) + "\n\n---\n" + task.Result
		} else {
			task.Result = string(content)
		}
	}

	// Stay on the task branch so the user can immediately review with
	// `git log`, `git diff main`, etc. No checkout back to baseBranch.
	//
	// Determine final status.
	// A task is "done" only when its commits are merged into the base branch.
	// If the LLM made commits but they're only on the task branch, status is "interrupted"
	// until the user merges to main. The status will flip to "done" on next skep tasks/status.
	decision := decideFinalStatus(llmErr, mergedToBase, hasCommits, hasResultFile, task.ID, taskBranch, baseBranch, resultFile)
	task.Status = decision.Status
	blog.Log("%s", decision.LogMsg)
	fmt.Fprint(os.Stderr, decision.UserMsg)

	if task.Result == "" {
		task.Result = "completed"
	}

	// Best-effort: parse the Claude session JSONL and record token usage
	// into the task row. Purely observability — never fails the task.
	if usage := ReadSessionUsage(root, task.SessionID); usage != nil {
		task.TokensUsed = usage.EffectiveBilledInput
		if b, err := json.Marshal(usage.ToolCallsByName); err == nil {
			task.ToolsUsedJSON = string(b)
		}
		fmt.Fprintf(os.Stderr, "skep: task #%d used %d effective input tokens across %d turns, %d tool calls\n",
			task.ID, usage.EffectiveBilledInput, usage.Turns, usage.ToolCalls)
	}

	return tasks.Update(store, task)
}

// ExecuteNonInteractive runs a task without TUI — used by the daemon.
// When task_steps rows are materialized, drives the per-step loop from
// step_executor.go. Falls back to the legacy single-shot shell-out when
// the task has no plan / no materialized steps.
func ExecuteNonInteractive(root, skepDir string, store *index.Store, cfg *config.Config, task *tasks.Task) error {
	baseBranch, err := GitCurrentBranch(root)
	if err != nil {
		baseBranch = "main"
	}

	branchName := task.Name
	if branchName == "" {
		branchName = slugify(task.Description)
	}
	taskBranch := fmt.Sprintf("skep/task-%d-%s", task.ID, branchName)

	if !GitIsClean(root) {
		return fmt.Errorf("dirty working tree")
	}
	// `--` prevents flag injection if taskBranch ever starts with `-`
	checkoutCmd := exec.Command("git", "checkout", "-B", taskBranch, "--")
	checkoutCmd.Dir = root
	if err := checkoutCmd.Run(); err != nil {
		return fmt.Errorf("git checkout -B %s: %w", taskBranch, err)
	}

	task.Status = tasks.StatusExecuting
	task.Branch = taskBranch
	tasks.Update(store, task)

	// Make sure task_steps is materialized before choosing the execution
	// path. MaterializeSteps is a no-op when rows already exist.
	_, _ = tasks.MaterializeSteps(store, task, cfg.StepModelByVerb)

	stepSum, _ := tasks.SummarizeSteps(store, task.ID)
	useStepExecution := stepSum != nil && stepSum.Total > 0

	var output string
	if useStepExecution {
		ok, stepErr := executeSteps(context.Background(), root, store, cfg, task)
		if stepErr != nil {
			task.Status = tasks.StatusFailed
			task.Result = "step executor failed: " + stepErr.Error()
			tasks.Update(store, task)
			GitCheckout(root, baseBranch)
			return stepErr
		}
		if !ok {
			// A step failed after retry — first-failure-stops-task policy.
			task.Status = tasks.StatusFailed
			task.Result = formatStepFailure(store, task.ID)
			tasks.Update(store, task)
			GitCheckout(root, baseBranch)
			return fmt.Errorf("task #%d: step failed", task.ID)
		}
		output = formatStepSummary(store, task.ID)
	} else {
		topSymbols, _ := store.TopSymbols(30)
		fc, _ := store.FileCount()
		sc, _ := store.SymbolCount()

		taskInfo := llm.TaskInfo{
			ID: task.ID, Name: task.Name,
			Description: task.Description, Plan: task.Plan, Acceptance: task.Acceptance,
		}
		promptCtx := llm.PromptContext{
			FileCount: fc, SymbolCount: sc, TopSymbols: topSymbols,
			TaskBranch: taskBranch, BaseBranch: baseBranch, TestCmd: cfg.TestCmd,
		}

		prompt := llm.BuildExecutionPrompt(taskInfo, promptCtx)

		output, err = llm.ShellOutQuiet(root, cfg.DaemonCmd(), prompt)
		if err != nil {
			retryPrompt := prompt + "\n\nPrevious attempt failed:\n" + output + "\n\nFix the issues."
			output, err = llm.ShellOutQuiet(root, cfg.DaemonCmd(), retryPrompt)
			if err != nil {
				task.Status = tasks.StatusFailed
				task.Result = "LLM failed: " + err.Error()
				tasks.Update(store, task)
				GitCheckout(root, baseBranch)
				return err
			}
		}
	}

	// Only run test_cmd when the (repoRoot, testCmd) pair is in the
	// out-of-repo trust store at ~/.skep/trusted-repos.json. Trust
	// intentionally lives OUTSIDE the repo so a committed config.json
	// cannot unilaterally enable execution on a collaborator's machine.
	// If the test_cmd has changed since the user last acknowledged it,
	// IsAllowed returns false and we skip — the user re-runs `skep init`
	// to re-approve.
	if cfg.TestCmd != "" {
		ok, trustErr := trust.IsAllowed(root, cfg.TestCmd)
		if trustErr != nil {
			fmt.Fprintf(os.Stderr, "skep: trust check failed: %v\n", trustErr)
		} else if !ok {
			fmt.Fprintf(os.Stderr, "skep: test_cmd not trusted for this repo — skipping. Run 'skep init' to re-approve.\n")
		} else {
			testOut, testErr := runTestCmd(root, cfg.TestCmd)
			if testErr != nil {
				task.Result = "Test failure: " + testOut
			}
		}
	}

	branchStore, indexErr := index.IndexBranch(root, skepDir, taskBranch)
	if indexErr == nil && branchStore != nil {
		diff, diffErr := index.DiffBranch(store, branchStore)
		branchStore.Close()
		if diffErr == nil && !diff.IsEmpty() {
			task.Result = formatDiff(diff)
		}
	}

	GitCheckout(root, baseBranch)

	hasCommits := gitHasCommits(root, baseBranch, taskBranch)
	mergedToBase := GitAllCommitsInBase(root, baseBranch, taskBranch)
	resultFile := filepath.Join(root, ".skep", fmt.Sprintf("task-%d-result.md", task.ID))
	hasResultFile := false
	if fi, statErr := os.Stat(resultFile); statErr == nil && fi.Size() > 0 {
		hasResultFile = true
	}
	decision := decideFinalStatus(nil, mergedToBase, hasCommits, hasResultFile, task.ID, taskBranch, baseBranch, resultFile)
	task.Status = decision.Status
	fmt.Fprint(os.Stderr, decision.UserMsg)
	if task.Result == "" {
		task.Result = output
	}
	return tasks.Update(store, task)
}

// finalDecision bundles the outcome of post-execution analysis: what status to
// persist, what to write to the branch log, and what to tell the user on stderr.
type finalDecision struct {
	Status  string
	LogMsg  string
	UserMsg string // printed to stderr (includes trailing newline)
}

// decideFinalStatus is the pure decision function for task completion status.
// It takes the observable outcome (LLM error, commit state, result file) and
// returns the status + messages to record. Kept pure for testability.
func decideFinalStatus(llmErr error, mergedToBase, hasCommits, hasResultFile bool, taskID int, taskBranch, baseBranch, resultFile string) finalDecision {
	if llmErr != nil {
		return finalDecision{
			Status:  tasks.StatusInterrupted,
			LogMsg:  fmt.Sprintf("task #%d interrupted: %v", taskID, llmErr),
			UserMsg: fmt.Sprintf("skep: task #%d interrupted. Resume: skep task run %d\n", taskID, taskID),
		}
	}
	if mergedToBase {
		return finalDecision{
			Status:  tasks.StatusDone,
			LogMsg:  fmt.Sprintf("task #%d done — commits merged into %s", taskID, baseBranch),
			UserMsg: fmt.Sprintf("skep: task #%d done (commits are in %s)\n", taskID, baseBranch),
		}
	}
	if hasCommits {
		return finalDecision{
			Status: tasks.StatusInterrupted,
			LogMsg: fmt.Sprintf("task #%d has commits on %s but not yet in %s", taskID, taskBranch, baseBranch),
			UserMsg: fmt.Sprintf("skep: task #%d has commits on %s. Merge into %s to mark done.\n  git checkout %s && git merge %s && git push\n",
				taskID, taskBranch, baseBranch, baseBranch, taskBranch),
		}
	}
	if hasResultFile {
		return finalDecision{
			Status:  tasks.StatusDone,
			LogMsg:  fmt.Sprintf("task #%d done — read-only result written", taskID),
			UserMsg: fmt.Sprintf("skep: task #%d done (read-only result in %s)\n", taskID, resultFile),
		}
	}
	return finalDecision{
		Status:  tasks.StatusInterrupted,
		LogMsg:  fmt.Sprintf("task #%d interrupted (no commits, no result file)", taskID),
		UserMsg: fmt.Sprintf("skep: task #%d interrupted. Resume: skep task run %d\n", taskID, taskID),
	}
}

// gitHasCommits checks if taskBranch has commits that baseBranch doesn't.
func gitHasCommits(root, baseBranch, taskBranch string) bool {
	cmd := exec.Command("git", "log", "--oneline", baseBranch+".."+taskBranch)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

func formatDiff(diff *index.IndexDiff) string {
	var b strings.Builder
	if len(diff.AddedSymbols) > 0 {
		b.WriteString("Added symbols:\n")
		for _, s := range diff.AddedSymbols {
			b.WriteString(fmt.Sprintf("  + %s (%s) in %s:%d\n", s.Name, s.Kind, s.FilePath, s.Line))
			if s.Signature != "" {
				b.WriteString(fmt.Sprintf("    %s\n", s.Signature))
			}
		}
	}
	if len(diff.RemovedSymbols) > 0 {
		b.WriteString("Removed symbols:\n")
		for _, s := range diff.RemovedSymbols {
			b.WriteString(fmt.Sprintf("  - %s (%s) in %s:%d\n", s.Name, s.Kind, s.FilePath, s.Line))
		}
	}
	if len(diff.ChangedSymbols) > 0 {
		b.WriteString("Changed symbols:\n")
		for _, c := range diff.ChangedSymbols {
			b.WriteString(fmt.Sprintf("  ~ %s (%s) in %s:%d\n", c.After.Name, c.After.Kind, c.After.FilePath, c.After.Line))
			b.WriteString(fmt.Sprintf("    was: %s\n", c.Before.Signature))
			b.WriteString(fmt.Sprintf("    now: %s\n", c.After.Signature))
		}
	}
	return b.String()
}

// runTestCmd executes the configured test command without a shell.
// Tokenizes via the same shlex-style parser the LLM shell-out uses —
// no `bash -c`, so test_cmd metacharacters can't be interpreted and
// the process tree does not include an interactive shell.
//
// test_cmd is expected to be a single command with arguments, e.g.
// `go test ./...` or `npm test -- --reporter=dot`. Shell features
// like pipelines, redirection, and backticks are not supported by
// design; callers needing that should wrap the pipeline in a script
// and invoke the script.
func runTestCmd(root, testCmd string) (string, error) {
	argv, err := llm.ShlexSplit(testCmd)
	if err != nil {
		return "", fmt.Errorf("parse test_cmd: %w", err)
	}
	if len(argv) == 0 {
		return "", fmt.Errorf("test_cmd is empty")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// formatStepSummary produces a human-readable summary of a task's
// completed steps, used as the task.Result when step-level execution
// succeeds end-to-end.
func formatStepSummary(store *index.Store, taskID int) string {
	steps, err := tasks.ListSteps(store, taskID)
	if err != nil || len(steps) == 0 {
		return "step execution completed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Step-level execution: %d steps\n", len(steps))
	for _, s := range steps {
		fmt.Fprintf(&b, "  %d. %s [%s]", s.Seq, s.Verb, s.Status)
		if s.CommitSHA != "" {
			fmt.Fprintf(&b, " commit=%s", s.CommitSHA)
		}
		if s.DurationMS > 0 {
			fmt.Fprintf(&b, " %.1fs", float64(s.DurationMS)/1000.0)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// formatStepFailure builds a task.Result summary when a step failed
// after retry. Includes the failed step's seq, verb, and truncated
// error output so `skep task show` surfaces the blocker directly.
func formatStepFailure(store *index.Store, taskID int) string {
	steps, err := tasks.ListSteps(store, taskID)
	if err != nil {
		return "step execution failed"
	}
	var failed *tasks.Step
	var done int
	for _, s := range steps {
		if s.Status == tasks.StepDone {
			done++
		}
		if s.Status == tasks.StepFailed && failed == nil {
			failed = s
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Step-level execution failed: %d/%d steps done\n", done, len(steps))
	if failed != nil {
		fmt.Fprintf(&b, "Failed at step %d/%s after %d retries\n", failed.Seq, failed.Verb, failed.RetryCount)
		if failed.Result != "" {
			b.WriteString("---\n")
			b.WriteString(failed.Result)
		}
	}
	return b.String()
}

// uuidRe matches Claude Code session IDs (UUID v4 format).
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isValidSessionID(id string) bool {
	return uuidRe.MatchString(id)
}

var nonAlpha = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlpha.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-")
}
