package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/llm"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
)

// maxStepRetries caps per-step retry attempts. v0.2.0 ships with one
// automatic retry after a failure, carrying the previous attempt's
// stderr/stdout into the retry prompt so the LLM can adjust. A second
// failure stops the task — the user investigates via `skep task show`.
const maxStepRetries = 1

// stepExecutionResult captures what happened during a step run.
// Used by executeSteps to decide whether to continue, retry, or stop.
type stepExecutionResult struct {
	Output      string
	CommitSHA   string // new HEAD if a commit landed, else ""
	DurationMS  int
	Err         error
}

// executeSteps drives the step-level task execution loop. It is the
// shared core used by both Execute (interactive) and ExecuteNonInteractive
// (daemon). For each pending step in seq order:
//
//  1. Mark step 'executing'
//  2. Resolve the LLM command (step.ModelOverride or cfg.DaemonCmd())
//  3. Build the per-step prompt
//  4. Shell out, capturing output + duration
//  5. On success: mark 'done', record commit SHA if HEAD advanced
//  6. On failure: retry once with error context, else mark 'failed' and stop
//
// Returns (true, nil) if all steps completed (or there were none to run),
// (false, nil) if a step failed without a retryable error (first-failure-
// stops-task policy), and (false, err) on an unrecoverable error.
//
// The caller is responsible for the surrounding task lifecycle (branch
// creation, post-exec re-index, final status decision). executeSteps
// only manages task_steps rows.
func executeSteps(ctx context.Context, root string, store *index.Store, cfg *config.Config, task *tasks.Task) (bool, error) {
	for {
		step, err := tasks.NextPendingStep(store, task.ID)
		if err != nil {
			return false, fmt.Errorf("next step: %w", err)
		}
		if step == nil {
			return true, nil // all steps terminal — task complete
		}

		// Mark step executing.
		step.Status = tasks.StepExecuting
		if err := tasks.UpdateStep(store, step); err != nil {
			return false, fmt.Errorf("update step %d: %w", step.Seq, err)
		}

		// Dispatch to the LLM.
		result := runStep(ctx, root, cfg, task, step)
		step.DurationMS = result.DurationMS
		step.Result = truncateOutput(result.Output, 8192)
		step.CommitSHA = result.CommitSHA

		// Success path.
		if result.Err == nil {
			step.Status = tasks.StepDone
			if err := tasks.UpdateStep(store, step); err != nil {
				return false, fmt.Errorf("mark step done: %w", err)
			}
			fmt.Fprintf(os.Stderr, "skep: step %d/%s → done%s\n", step.Seq, step.Verb, commitSuffix(step.CommitSHA))
			continue
		}

		// Retry once, carrying error context into the retry prompt.
		if step.RetryCount < maxStepRetries {
			step.RetryCount++
			fmt.Fprintf(os.Stderr, "skep: step %d/%s failed, retrying (attempt %d): %v\n",
				step.Seq, step.Verb, step.RetryCount+1, result.Err)

			retry := runStepRetry(ctx, root, cfg, task, step, result.Err, result.Output)
			step.DurationMS += retry.DurationMS
			step.Result = truncateOutput(retry.Output, 8192)
			if retry.CommitSHA != "" {
				step.CommitSHA = retry.CommitSHA
			}

			if retry.Err == nil {
				step.Status = tasks.StepDone
				if err := tasks.UpdateStep(store, step); err != nil {
					return false, fmt.Errorf("mark step done after retry: %w", err)
				}
				fmt.Fprintf(os.Stderr, "skep: step %d/%s → done (after retry)%s\n", step.Seq, step.Verb, commitSuffix(step.CommitSHA))
				continue
			}

			// Retry also failed.
			result.Err = retry.Err
		}

		// First-failure-stops-task: mark failed, return false to caller.
		step.Status = tasks.StepFailed
		step.Result = fmt.Sprintf("%s\n\n---\nfinal error: %v", step.Result, result.Err)
		if uerr := tasks.UpdateStep(store, step); uerr != nil {
			return false, fmt.Errorf("mark step failed: %w", uerr)
		}
		fmt.Fprintf(os.Stderr, "skep: step %d/%s → failed: %v\n", step.Seq, step.Verb, result.Err)
		return false, nil
	}
}

// runStep performs one shell-out for a step. It captures HEAD before
// and after so the caller can detect whether the LLM committed.
func runStep(ctx context.Context, root string, cfg *config.Config, task *tasks.Task, step *tasks.Step) stepExecutionResult {
	headBefore := gitHead(root)
	start := time.Now()

	llmCmd := resolveStepCommand(cfg, step)
	prompt := buildStepPrompt(task, step)

	output, err := llm.ShellOutQuietCtx(ctx, root, llmCmd, prompt)
	duration := int(time.Since(start).Milliseconds())

	result := stepExecutionResult{
		Output:     output,
		DurationMS: duration,
		Err:        err,
	}
	if headAfter := gitHead(root); headAfter != "" && headAfter != headBefore {
		result.CommitSHA = headAfter
	}
	return result
}

// runStepRetry shells out a second time with the previous attempt's
// error + output included in the prompt, so the model can correct
// course instead of repeating the same mistake.
func runStepRetry(ctx context.Context, root string, cfg *config.Config, task *tasks.Task, step *tasks.Step, prevErr error, prevOutput string) stepExecutionResult {
	headBefore := gitHead(root)
	start := time.Now()

	llmCmd := resolveStepCommand(cfg, step)
	prompt := buildStepPrompt(task, step) + fmt.Sprintf(
		"\n\n---\nPrevious attempt failed: %v\n\nPrevious output (truncated):\n%s\n\nFix the issue and complete the step.",
		prevErr, truncateOutput(prevOutput, 2048),
	)

	output, err := llm.ShellOutQuietCtx(ctx, root, llmCmd, prompt)
	duration := int(time.Since(start).Milliseconds())

	result := stepExecutionResult{
		Output:     output,
		DurationMS: duration,
		Err:        err,
	}
	if headAfter := gitHead(root); headAfter != "" && headAfter != headBefore {
		result.CommitSHA = headAfter
	}
	return result
}

// resolveStepCommand picks the LLM command for a step. If the step has a
// ModelOverride populated from config.StepModelByVerb, it is injected
// into the base command via the same path used for classify/plan models.
// Otherwise falls back to cfg.DaemonCmd() (the non-interactive main LLM
// command).
func resolveStepCommand(cfg *config.Config, step *tasks.Step) string {
	base := cfg.DaemonCmd()
	if step.ModelOverride == "" {
		return base
	}
	return injectModelFlag(base, step.ModelOverride)
}

// injectModelFlag rewrites --model <value> in an LLM command template,
// adding the flag if it is missing. Mirrors the config package's
// injectModel helper but kept local so the executor does not take an
// indirect dependency on unexported config internals.
func injectModelFlag(cmd, model string) string {
	if model == "" {
		return cmd
	}
	// Already has --model? Replace the value.
	if idx := strings.Index(cmd, "--model "); idx >= 0 {
		rest := cmd[idx+len("--model "):]
		// Find the end of the existing value (whitespace or end of string).
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			end = len(rest)
		}
		return cmd[:idx+len("--model ")] + model + rest[end:]
	}
	// Append --model <value> to the base command.
	return strings.TrimSpace(cmd) + " --model " + model
}

// buildStepPrompt renders a focused prompt for a single step. The
// executor dumps task context + the current step's verb, target,
// symbols, and acceptance criteria, plus a standing instruction to
// commit work at the end of the step so the commit SHA capture can
// record per-step progress.
func buildStepPrompt(task *tasks.Task, step *tasks.Step) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task #%d: %s\n\n", task.ID, task.Description)
	if task.Acceptance != "" {
		fmt.Fprintf(&b, "## Task acceptance\n%s\n\n", task.Acceptance)
	}
	fmt.Fprintf(&b, "## Step %d of this task\n", step.Seq)
	fmt.Fprintf(&b, "**Verb:** %s\n", step.Verb)
	if step.TargetFile != "" {
		fmt.Fprintf(&b, "**Target file:** %s\n", step.TargetFile)
	}
	if len(step.Symbols) > 0 {
		fmt.Fprintf(&b, "**Symbols:** %s\n", strings.Join(step.Symbols, ", "))
	}
	if step.Acceptance != "" {
		fmt.Fprintf(&b, "**Step acceptance:** %s\n", step.Acceptance)
	}
	if step.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", step.Description)
	}
	b.WriteString("\n---\n")
	b.WriteString("Complete only this step. Do not work ahead to later steps — they will run in follow-up invocations with their own context. When the step is done, commit your changes with a message describing this step (e.g. 'step ")
	fmt.Fprintf(&b, "%d: %s')", step.Seq, step.Verb)
	b.WriteString(".\n")
	return b.String()
}

// gitHead returns the current HEAD commit SHA (short form) or "" if git
// fails. Used before/after each step to detect whether the LLM committed.
func gitHead(root string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// truncateOutput caps LLM output at n bytes, appending a marker if
// truncation occurred. Keeps task_steps.result from growing unbounded.
func truncateOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...[truncated]"
}

// commitSuffix renders a short " (commit <sha>)" suffix for stderr
// progress lines, or "" when no commit landed.
func commitSuffix(sha string) string {
	if sha == "" {
		return ""
	}
	return " (commit " + sha + ")"
}
