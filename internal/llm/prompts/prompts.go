// Package prompts renders LLM prompts from embedded text/template files.
//
// Prompts are content, not code. They live as .tmpl files under this package,
// are embedded into the binary via //go:embed (so the single-binary story is
// preserved), and rendered with typed context structs so placeholders remain
// compile-checked at the call site.
package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed *.tmpl
var files embed.FS

var funcs = template.FuncMap{
	"join": strings.Join,
}

var (
	dedupTmpl    = template.Must(template.New("dedup.tmpl").Funcs(funcs).ParseFS(files, "dedup.tmpl"))
	classifyTmpl = template.Must(template.New("classify.tmpl").Funcs(funcs).ParseFS(files, "classify.tmpl"))
	planTmpl     = template.Must(template.New("plan.tmpl").Funcs(funcs).ParseFS(files, "plan.tmpl"))
	executeTmpl  = template.Must(template.New("execute.tmpl").Funcs(funcs).ParseFS(files, "execute.tmpl"))
)

// SymbolRef is the minimal symbol shape the templates need.
// Defined here to avoid pulling the index package into template data.
type SymbolRef struct {
	Name      string
	Kind      string
	FilePath  string
	Line      int
	Signature string
}

// ExistingTaskRef is a compact reference to a currently-active task in the
// same repo. Passed to the dedup phase so an LLM can catch semantic
// duplicates that the BM25 pre-filter missed (e.g. paraphrases with zero
// token overlap like "fix login bug" vs "repair auth issue").
type ExistingTaskRef struct {
	ID          int
	Status      string
	Description string
}

// DedupData is the context for rendering dedup.tmpl — BM25-pre-filtered
// candidate tasks + the new task description. The LLM returns whether the
// new task is a semantic duplicate of any candidate.
type DedupData struct {
	Task       string
	Candidates []ExistingTaskRef
}

// ClassifyData is the context for rendering classify.tmpl.
type ClassifyData struct {
	FileCount   int
	SymbolCount int
	TopSymbols  []SymbolRef
	Peers       []string
	Task        string
}

// PlanData is the context for rendering plan.tmpl. Same shape as
// ClassifyData today — the plan prompt uses the identical static
// context slice plus MCP tools for dynamic exploration. Kept as a
// separate type so the schemas can diverge (e.g. add plan-specific
// fields like RelatedPRs or RecentCommits) without breaking classify.
type PlanData struct {
	FileCount   int
	SymbolCount int
	TopSymbols  []SymbolRef
	Peers       []string
	Task        string
}

// ExecuteData is the context for rendering execute.tmpl.
type ExecuteData struct {
	TaskID          int
	TaskName        string
	TaskDescription string
	Plan            string         // legacy prose rendering (fallback)
	PlanSteps       []PlanStepView // structured steps (preferred when non-empty)
	Acceptance      string
	FileCount       int
	SymbolCount     int
	Languages       []string
	FileTree        []string
	TopSymbols      []SymbolRef
	RelevantEdges   []string
	RecentCommits   []string
	TaskBranch      string
	BaseBranch      string
	TestCmd         string
}

// PlanStepView is the template-facing flattened view of a structured
// plan step. Defined in prompts/ to avoid a circular import of
// internal/tasks from internal/llm/prompts — the caller projects its
// `tasks.PlanStep` into this shape at render time.
type PlanStepView struct {
	Index       int
	Verb        string
	TargetFile  string
	Symbols     []string
	Acceptance  string
	Description string
	DependsOn   []int
}

// Dedup renders the semantic-duplicate-check prompt.
func Dedup(d DedupData) (string, error) {
	return render(dedupTmpl, d)
}

// Classify renders the classification prompt.
func Classify(d ClassifyData) (string, error) {
	return render(classifyTmpl, d)
}

// Plan renders the structured plan-generation prompt.
func Plan(d PlanData) (string, error) {
	return render(planTmpl, d)
}

// Execute renders the execution prompt.
func Execute(d ExecuteData) (string, error) {
	return render(executeTmpl, d)
}

func render(t *template.Template, data any) (string, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render %s: %w", t.Name(), err)
	}
	return b.String(), nil
}
