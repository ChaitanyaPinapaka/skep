package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/llm"
	"github.com/ChaitanyaPinapaka/skep/internal/llm/prompts"
)

// PlanStep is one structured step in an execution plan. Unlike the
// old free-form `plan []string`, each step now carries enough metadata
// that the executor can reason about it without re-parsing prose:
//
//   - Verb — what kind of action this is, which drives executor routing
//     (e.g. delegate → MCP call, test → cmd run, add|modify → edit)
//   - TargetFile — the single file this step is responsible for
//   - Symbols — the specific functions/types touched, grounded via MCP
//     search_symbols during plan generation so they're real, not
//     hallucinated
//   - Acceptance — a short, verifiable "how do we know this step is
//     done" sentence. Not a test command, just the definition-of-done.
//   - DependsOn — indices of prior steps that must complete first.
//     Enables the executor to parallelize independent steps and stop
//     on failure of an upstream step.
type PlanStep struct {
	Verb       string   `json:"verb"`
	TargetFile string   `json:"target_file,omitempty"`
	Symbols    []string `json:"symbols,omitempty"`
	Acceptance string   `json:"acceptance,omitempty"`
	DependsOn  []int    `json:"depends_on,omitempty"`
	// Description is a free-form sentence that captures the full
	// instruction for the executor. Populated even when verb/target/
	// acceptance are set, so downstream formatters have a readable line.
	Description string `json:"description,omitempty"`
}

// PlanResult is what the plan-generation goroutine returns. Separate
// from ClassificationResult so the two goroutines can run in parallel
// without sharing state.
type PlanResult struct {
	Steps           []PlanStep `json:"plan"`
	FilesAffected   []string   `json:"files_affected,omitempty"`
	SymbolsAffected []string   `json:"symbols_affected,omitempty"`
}

// PipelineResult is the merged output of classify + plan-gen.
// Either half may be empty when the other rejected/clarified before
// it completed. The caller decides how to interpret a partial result.
type PipelineResult struct {
	// From classifier:
	Classification      string   `json:"classification"` // small | large | ambiguous | reject
	Reason              string   `json:"reason"`
	Confidence          float64  `json:"confidence"`
	NeedsClarification  bool     `json:"needs_clarification,omitempty"`
	ClarifyingQuestions []string `json:"clarifying_questions,omitempty"`
	RejectReason        string   `json:"reject_reason,omitempty"`

	// From plan-gen:
	Plan            []PlanStep `json:"plan,omitempty"`
	FilesAffected   []string   `json:"files_affected,omitempty"`
	SymbolsAffected []string   `json:"symbols_affected,omitempty"`

	// Errors from either half, kept for observability.
	ClassifyErr error `json:"-"`
	PlanErr     error `json:"-"`
}

// ToLegacyClassification projects a PipelineResult into the older
// ClassificationResult shape for call sites that haven't migrated to
// the structured plan yet. Preserves the plan as stringified steps
// (verb + description + target) so FormatPlan still works.
func (p *PipelineResult) ToLegacyClassification() *ClassificationResult {
	steps := make([]string, 0, len(p.Plan))
	for _, s := range p.Plan {
		steps = append(steps, renderStepLine(s))
	}
	return &ClassificationResult{
		Classification:  p.Classification,
		Reason:          p.Reason,
		FilesAffected:   p.FilesAffected,
		SymbolsAffected: p.SymbolsAffected,
		Plan:            steps,
	}
}

// renderStepLine turns a PlanStep into a single human-readable line
// for storage/display. Prefers the model's own Description when set,
// falls back to a verb+target reconstruction so empty descriptions
// never produce empty plan lines.
func renderStepLine(s PlanStep) string {
	if s.Description != "" {
		return s.Description
	}
	var b strings.Builder
	if s.Verb != "" {
		b.WriteString(strings.ToUpper(s.Verb[:1]) + s.Verb[1:])
		b.WriteByte(' ')
	}
	if s.TargetFile != "" {
		b.WriteString(s.TargetFile)
	}
	if len(s.Symbols) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(s.Symbols, ", "))
		b.WriteByte(')')
	}
	if s.Acceptance != "" {
		b.WriteString(" — ")
		b.WriteString(s.Acceptance)
	}
	return strings.TrimSpace(b.String())
}

// planMCPEnabled returns true when the plan-generation shell-out
// should attach the Skep MCP server. Defaults to ON — grounded plans
// are materially better than ones built from the static context slice.
// Opt out with SKEP_PLAN_MCP=0/false/no/off for offline demos, CI runs,
// or debugging the static-context prompt in isolation.
func planMCPEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SKEP_PLAN_MCP")))
	switch v {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// GeneratePlanCtx is the plan-generation half of the pipeline. Separate
// from ClassifyTaskCtx so it can run concurrently. Uses the plan.tmpl
// template (structured output schema) and, by default, the MCP-attached
// shell-out so the model can call search_symbols / get_file_context /
// get_call_graph while drafting the plan.
//
// Returns (nil, ctx.Err()) when ctx is cancelled — used by the merge
// logic to abort an in-flight plan when classify rejects the task.
func GeneratePlanCtx(ctx context.Context, store *index.Store, repoRoot, llmCmd string, task *Task) (*PlanResult, error) {
	if llmCmd == "" {
		return nil, fmt.Errorf("plan: llm-cmd unset")
	}

	planCmd := llm.ResolveClassify(llmCmd)
	if planCmd == llmCmd {
		planCmd = classifyCommand(llmCmd)
	}

	data := buildClassifyData(store, repoRoot, task.Description)
	prompt, err := prompts.Plan(prompts.PlanData{
		FileCount:   data.FileCount,
		SymbolCount: data.SymbolCount,
		TopSymbols:  data.TopSymbols,
		Peers:       data.Peers,
		Task:        data.Task,
	})
	if err != nil {
		return nil, fmt.Errorf("render plan prompt: %w", err)
	}

	var output string
	if planMCPEnabled() {
		output, err = llm.ShellOutQuietWithMCP(ctx, repoRoot, planCmd, prompt)
	} else {
		output, err = llm.ShellOutQuietCtx(ctx, repoRoot, planCmd, prompt)
	}
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	result := extractPlanJSON(output)
	if result == nil {
		return nil, fmt.Errorf("no valid plan JSON in LLM response.\nraw output (first 2000 chars):\n%s", truncate(output, 2000))
	}
	return result, nil
}

// extractPlanJSON walks the LLM output for the first JSON object that
// parses as a PlanResult with at least one step. Mirrors
// extractClassifyJSON's strict-walk pattern so envelope wrappers and
// narrative prose don't silently produce empty plans.
func extractPlanJSON(s string) *PlanResult {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		dec.UseNumber()
		var candidate PlanResult
		if err := dec.Decode(&candidate); err == nil && len(candidate.Steps) > 0 {
			return &candidate
		}
	}
	return nil
}

// ClassifyAndPlanCtx runs the classifier and plan-generator concurrently
// and merges their results. This is the Stage 2+3 pipeline entry point.
//
// Semantics:
//   - Both goroutines share the passed context.
//   - If classify finishes first and returns needs_clarification or a
//     reject verdict, the plan context is cancelled and its result is
//     discarded.
//   - If plan finishes first, the merge waits for classify (needed to
//     decide small/large/clarify), then merges.
//   - Either half erroring does NOT cancel the other; the caller gets
//     both errors via PipelineResult.ClassifyErr / PlanErr and decides
//     how to degrade.
//
// This function is wired into `skep task create` as the single
// replacement for the old sequential classify-then-plan call.
func ClassifyAndPlanCtx(ctx context.Context, store *index.Store, repoRoot, classifyCmd, planCmd string, task *Task) (*PipelineResult, error) {
	planCtx, cancelPlan := context.WithCancel(ctx)
	defer cancelPlan()

	var (
		wg          sync.WaitGroup
		classifyRes *ClassificationResult
		classifyErr error
		planRes     *PlanResult
		planErr     error
	)

	wg.Add(2)

	// Classifier goroutine.
	go func() {
		defer wg.Done()
		classifyRes, classifyErr = ClassifyTaskCtx(ctx, store, repoRoot, classifyCmd, task)
		// If the classifier says reject or needs clarification, there's
		// no point letting the plan call burn tokens. Cancel it.
		if classifyRes != nil {
			if classifyRes.Classification == "reject" || classifyRes.Classification == "ambiguous" {
				cancelPlan()
			}
		}
	}()

	// Plan-generation goroutine.
	go func() {
		defer wg.Done()
		planRes, planErr = GeneratePlanCtx(planCtx, store, repoRoot, planCmd, task)
	}()

	wg.Wait()

	return mergePipeline(classifyRes, classifyErr, planRes, planErr), nil
}

// mergePipeline fuses the two goroutines' results into a single
// PipelineResult. Handles the partial-failure cases explicitly so the
// caller never has to second-guess which half succeeded.
func mergePipeline(c *ClassificationResult, cErr error, p *PlanResult, pErr error) *PipelineResult {
	out := &PipelineResult{
		ClassifyErr: cErr,
		PlanErr:     pErr,
	}
	if c != nil {
		out.Classification = c.Classification
		out.Reason = c.Reason
		out.NeedsClarification = c.NeedsClarification
		out.ClarifyingQuestions = c.ClarifyingQuestions
		out.RejectReason = c.RejectReason
		out.Confidence = c.Confidence
		// Legacy files/symbols from the classifier; plan overrides if set.
		out.FilesAffected = c.FilesAffected
		out.SymbolsAffected = c.SymbolsAffected
	}
	if p != nil {
		out.Plan = p.Steps
		if len(p.FilesAffected) > 0 {
			out.FilesAffected = p.FilesAffected
		}
		if len(p.SymbolsAffected) > 0 {
			out.SymbolsAffected = p.SymbolsAffected
		}
	}
	// Classifier is authoritative for the sizing decision. If it's
	// missing entirely (hard failure), fall back to "ambiguous" so the
	// caller surfaces the errors instead of auto-approving blindly.
	if out.Classification == "" {
		out.Classification = "ambiguous"
		if cErr != nil {
			out.Reason = fmt.Sprintf("classify failed: %v", cErr)
		}
	}
	// If the classifier intentionally cancelled the plan goroutine
	// (needs_clarification or reject), the plan shell-out dies with
	// `signal: killed`. That's expected behavior, not a failure —
	// clear the PlanErr so FormatPipelineResult doesn't show a
	// misleading "Plan error" line to the user.
	if pErr != nil && (out.NeedsClarification || out.Classification == "reject") {
		out.PlanErr = nil
	}
	return out
}

// FormatPipelineResult renders a PipelineResult as human-readable text
// for storage in task.Plan and display in `skep task show`. Mirrors
// FormatPlan but uses structured steps when available.
func FormatPipelineResult(r *PipelineResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Classification: %s\nReason: %s\n", r.Classification, r.Reason)
	if r.Confidence > 0 {
		fmt.Fprintf(&b, "Confidence: %.2f\n", r.Confidence)
	}
	if r.NeedsClarification && len(r.ClarifyingQuestions) > 0 {
		b.WriteString("Clarifying questions:\n")
		for i, q := range r.ClarifyingQuestions {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, q)
		}
	}
	if r.RejectReason != "" {
		fmt.Fprintf(&b, "Reject reason: %s\n", r.RejectReason)
	}
	if len(r.FilesAffected) > 0 {
		fmt.Fprintf(&b, "Files: %s\n", strings.Join(r.FilesAffected, ", "))
	}
	if len(r.SymbolsAffected) > 0 {
		fmt.Fprintf(&b, "Symbols: %s\n", strings.Join(r.SymbolsAffected, ", "))
	}
	if len(r.Plan) > 0 {
		b.WriteString("Plan:\n")
		for i, s := range r.Plan {
			fmt.Fprintf(&b, "  %d. %s", i+1, renderStepLine(s))
			if len(s.DependsOn) > 0 {
				fmt.Fprintf(&b, "  [after %v]", s.DependsOn)
			}
			b.WriteByte('\n')
		}
	}
	if r.ClassifyErr != nil {
		fmt.Fprintf(&b, "Classify error: %v\n", r.ClassifyErr)
	}
	if r.PlanErr != nil {
		fmt.Fprintf(&b, "Plan error: %v\n", r.PlanErr)
	}
	return b.String()
}
