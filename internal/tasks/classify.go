package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/llm"
	"github.com/ChaitanyaPinapaka/skep/internal/llm/prompts"
	"github.com/ChaitanyaPinapaka/skep/internal/registry"
)

// ClassificationResult holds the LLM classifier's decision. In the
// Stage 2+3 parallel pipeline this carries ONLY the classification
// half — sizing, confidence, and clarify/reject signals. The plan
// comes from a separate parallel call (see plan.go).
//
// Legacy fields FilesAffected / SymbolsAffected / Plan are retained
// so older callers and fallback paths still compile; they are left
// empty by the new classify.tmpl and populated only by the plan
// merge layer via ToLegacyClassification().
type ClassificationResult struct {
	Classification      string   `json:"classification"` // small | large | ambiguous | reject
	Reason              string   `json:"reason"`
	Confidence          float64  `json:"confidence,omitempty"`
	NeedsClarification  bool     `json:"needs_clarification,omitempty"`
	ClarifyingQuestions []string `json:"clarifying_questions,omitempty"`
	RejectReason        string   `json:"reject_reason,omitempty"`

	// Legacy / merge-populated fields — not emitted by the new
	// classify.tmpl. Retained so FormatPlan and existing call sites
	// keep working.
	FilesAffected   []string `json:"files_affected,omitempty"`
	SymbolsAffected []string `json:"symbols_affected,omitempty"`
	Plan            []string `json:"plan,omitempty"`
}

// ClassifyTask shells out to the LLM CLI to classify a task and generate a plan.
// Returns the classification result. If LLM is not configured, returns nil (skip classification).
// The context is not cancellable — use ClassifyTaskCtx from daemons so shutdown
// can kill an in-flight classifier call.
func ClassifyTask(store *index.Store, repoRoot, llmCmd string, task *Task) (*ClassificationResult, error) {
	return ClassifyTaskCtx(context.Background(), store, repoRoot, llmCmd, task)
}

// ClassifyTaskCtx is the cancellable variant. When ctx is cancelled (e.g.
// daemon shutdown), the child LLM process is killed and this returns with
// ctx.Err() wrapped.
//
// When SKEP_CLASSIFY_MCP=1 (or config key classify-mcp=true), the classifier
// shell-out attaches the Skep MCP server so the model can dynamically call
// search_symbols, get_file_context, and get_call_graph during classification.
// This trades higher per-call cost (multiple tool-call round trips) for a
// grounded plan that references real symbols instead of hallucinated ones.
func ClassifyTaskCtx(ctx context.Context, store *index.Store, repoRoot, llmCmd string, task *Task) (*ClassificationResult, error) {
	if llmCmd == "" {
		return nil, nil
	}

	// Use the classify template (non-interactive, read-only)
	classifyCmd := llm.ResolveClassify(llmCmd)
	// If it resolved to the same thing (custom template), strip interactive flags
	if classifyCmd == llmCmd {
		classifyCmd = classifyCommand(llmCmd)
	}

	data := buildClassifyData(store, repoRoot, task.Description)
	prompt, err := prompts.Classify(data)
	if err != nil {
		return nil, fmt.Errorf("render classify prompt: %w", err)
	}

	// Decide whether to attach the Skep MCP server. Opt-in for now — the
	// tool-less path is cheaper and good enough for most repos; the MCP
	// path is worth it on large or unfamiliar codebases where the static
	// context slice is too narrow.
	useMCP := classifyMCPEnabled()
	var output string
	if useMCP {
		output, err = llm.ShellOutQuietWithMCP(ctx, repoRoot, classifyCmd, prompt)
	} else {
		output, err = llm.ShellOutQuietCtx(ctx, repoRoot, classifyCmd, prompt)
	}
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}

	// Parse JSON response. extractClassifyJSON is strict — it walks every
	// '{' in the output and only accepts a parsed object whose
	// `classification` field is non-empty. This avoids silently accepting
	// Claude Code output envelopes ({"content":[...]}) or stray JSON-like
	// prose fragments that would otherwise deserialize to a zero-value
	// ClassificationResult.
	result := extractClassifyJSON(output)
	if result == nil {
		return nil, fmt.Errorf("no valid classification JSON in LLM response.\n"+
			"This usually means the LLM wrote prose instead of the required JSON object, "+
			"or it wrapped its response in a format Skep doesn't recognize.\n"+
			"raw output (first 2000 chars):\n%s", truncate(output, 2000))
	}

	return result, nil
}

// truncate cuts a string to n runes with an ellipsis marker. Used for
// error messages that need bounded length.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}

// buildClassifyData assembles the template context for the classify prompt.
// The classifier only needs a minimal slice of the index — top 20 symbols
// with truncated signatures — because its job is scope-level reasoning, not
// implementation detail. Keep this ~2k tokens, not ~10k.
func buildClassifyData(store *index.Store, repoRoot, taskDescription string) prompts.ClassifyData {
	fc, _ := store.FileCount()
	sc, _ := store.SymbolCount()

	syms, _ := store.TopSymbols(20)
	top := make([]prompts.SymbolRef, 0, len(syms))
	for _, s := range syms {
		top = append(top, prompts.SymbolRef{
			Name:      s.Name,
			Kind:      s.Kind,
			FilePath:  s.FilePath,
			Line:      s.Line,
			Signature: truncateSignature(s.Signature, 120),
		})
	}

	return prompts.ClassifyData{
		FileCount:   fc,
		SymbolCount: sc,
		TopSymbols:  top,
		Peers:       loadPeerRepos(repoRoot),
		Task:        taskDescription,
	}
}

// truncateSignature caps a signature at n characters so classifier context
// stays bounded even on repos with very long generic / template lines.
func truncateSignature(sig string, n int) string {
	if len(sig) <= n {
		return sig
	}
	return sig[:n] + "…"
}

// loadPeerRepos returns the names of other agents in this workspace (excluding self).
// Peers are the signal the classifier uses to decide whether a task should be split
// via create_remote_task.
func loadPeerRepos(repoRoot string) []string {
	reg, err := registry.LoadFrom(repoRoot)
	if err != nil || reg == nil || len(reg.Agents) == 0 {
		return nil
	}
	absSelf, _ := filepath.Abs(repoRoot)
	var peers []string
	for name, a := range reg.Agents {
		if a == nil {
			continue
		}
		absPeer, _ := filepath.Abs(a.Path)
		if absPeer == absSelf {
			continue
		}
		peers = append(peers, name)
	}
	return peers
}

// FormatPlan formats a classification result as a human-readable plan string.
func FormatPlan(r *ClassificationResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Classification: %s\nReason: %s\n", r.Classification, r.Reason))
	if len(r.FilesAffected) > 0 {
		b.WriteString(fmt.Sprintf("Files: %s\n", strings.Join(r.FilesAffected, ", ")))
	}
	if len(r.SymbolsAffected) > 0 {
		b.WriteString(fmt.Sprintf("Symbols: %s\n", strings.Join(r.SymbolsAffected, ", ")))
	}
	if len(r.Plan) > 0 {
		b.WriteString("Plan:\n")
		for i, step := range r.Plan {
			b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, step))
		}
	}
	return b.String()
}

// classifyMCPEnabled returns true when the classifier should attach the
// Skep MCP server to its shell-out so the model can query the index
// dynamically during classification. Off by default; opt in via
// SKEP_CLASSIFY_MCP=1 (any truthy value).
func classifyMCPEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SKEP_CLASSIFY_MCP")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// classifyCommand strips execution-only flags from the LLM command template.
// Classification is read-only — no file editing, no JSON output envelope.
func classifyCommand(cmd string) string {
	// Remove --allowedTools and its argument
	for _, prefix := range []string{`--allowedTools "Edit,Write,Bash"`, `--allowedTools Edit,Write,Bash`, `--allowed-tools "Edit,Write,Bash"`} {
		cmd = strings.ReplaceAll(cmd, prefix, "")
	}
	// Remove --output-format json (Claude wraps response in envelope, breaks JSON extraction)
	cmd = strings.ReplaceAll(cmd, "--output-format json", "")
	cmd = strings.ReplaceAll(cmd, "--output-format=json", "")
	// Clean up double spaces
	for strings.Contains(cmd, "  ") {
		cmd = strings.ReplaceAll(cmd, "  ", " ")
	}
	return strings.TrimSpace(cmd)
}

// extractJSON walks every '{' in s and returns the first complete JSON
// object as a raw string. Used by callers that need flexible parsing
// (e.g. dedup, where the expected shape is already known and we'll just
// swallow any parse failure silently as non-duplicate).
//
// Stricter extraction (validating that the parsed object has the
// expected fields) is done by extractClassifyJSON below.
func extractJSON(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil && len(raw) > 0 {
			return string(raw)
		}
	}
	return ""
}

// extractClassifyJSON finds the first JSON object in s that parses as a
// valid ClassificationResult — i.e. one with a non-empty `classification`
// field. Returns the decoded result, or nil if no candidate matches.
//
// This is deliberately stricter than a generic JSON extractor:
//
//   - Claude Code can wrap the model's response in an envelope like
//     {"content": [...], "usage": {...}} which parses as valid JSON but
//     has none of our expected fields. A lax "first valid JSON" parser
//     would accept the envelope and silently return an empty result.
//   - The LLM itself sometimes embeds a stray JSON-looking object in its
//     narrative ("{error, details}") before or after the real classify
//     object.
//
// So we walk every '{' in the output, try to decode a ClassificationResult,
// and accept only the first candidate whose `classification` field is
// populated. If none match, we fall through to nil and the caller surfaces
// an error with the raw output.
func extractClassifyJSON(s string) *ClassificationResult {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		dec.UseNumber()
		var candidate ClassificationResult
		if err := dec.Decode(&candidate); err == nil && candidate.Classification != "" {
			return &candidate
		}
	}
	return nil
}
