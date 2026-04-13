package llm

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/llm/prompts"
)

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// BuildExecutionPrompt renders the execution prompt via the embedded template.
// The template lives at internal/llm/prompts/execute.tmpl.
func BuildExecutionPrompt(task TaskInfo, ctx PromptContext) string {
	top := make([]prompts.SymbolRef, 0, len(ctx.TopSymbols))
	for _, s := range ctx.TopSymbols {
		top = append(top, prompts.SymbolRef{
			Name:      s.Name,
			Kind:      s.Kind,
			FilePath:  s.FilePath,
			Line:      s.Line,
			Signature: s.Signature,
		})
	}

	out, err := prompts.Execute(prompts.ExecuteData{
		TaskID:          task.ID,
		TaskName:        task.Name,
		TaskDescription: task.Description,
		Plan:            task.Plan,
		PlanSteps:       decodePlanSteps(task.PlanStepsJSON),
		Acceptance:      task.Acceptance,
		FileCount:       ctx.FileCount,
		SymbolCount:     ctx.SymbolCount,
		Languages:       ctx.Languages,
		FileTree:        ctx.FileTree,
		TopSymbols:      top,
		RelevantEdges:   ctx.RelevantEdges,
		RecentCommits:   ctx.RecentCommits,
		TaskBranch:      ctx.TaskBranch,
		BaseBranch:      ctx.BaseBranch,
		TestCmd:         ctx.TestCmd,
	})
	if err != nil {
		// Template rendering is a programmer error — templates are embedded and
		// validated at startup via template.Must. Fall back to a minimal prompt
		// so execution still proceeds instead of silently failing.
		return fmt.Sprintf("<task><id>%d</id><description>%s</description></task>\nprompt render failed: %v", task.ID, task.Description, err)
	}
	return out
}

// BuildSystemPrompt constructs the system prompt appended to Claude's default.
func BuildSystemPrompt(task TaskInfo) string {
	return fmt.Sprintf(`You are executing skep task #%d: "%s"

You are a senior engineer making targeted changes to this codebase. Your job is to:
- Implement exactly what the task asks for — no more, no less
- Create a clean git branch with atomic, well-described commits
- Write production-quality code that matches the existing style and patterns
- Use skep MCP tools to explore the codebase before editing
- Run tests if a test command is provided

You have full access to edit files, run commands, and use git.`, task.ID, task.Name)
}

// TaskInfo holds the task data needed for prompt building.
type TaskInfo struct {
	ID            int
	Name          string
	Description   string
	Plan          string // prose fallback
	PlanStepsJSON string // serialized []tasks.PlanStep from the pipeline
	Acceptance    string
}

// decodePlanSteps parses a task's PlanStepsJSON into the template-facing
// PlanStepView shape. Returns nil when the field is empty or invalid —
// the template falls back to the prose Plan in that case, so a parse
// failure degrades to the pre-E1 behavior rather than erroring out.
func decodePlanSteps(raw string) []prompts.PlanStepView {
	if raw == "" {
		return nil
	}
	var steps []struct {
		Verb        string   `json:"verb"`
		TargetFile  string   `json:"target_file"`
		Symbols     []string `json:"symbols"`
		Acceptance  string   `json:"acceptance"`
		DependsOn   []int    `json:"depends_on"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil
	}
	views := make([]prompts.PlanStepView, len(steps))
	for i, s := range steps {
		views[i] = prompts.PlanStepView{
			Index:       i + 1,
			Verb:        s.Verb,
			TargetFile:  s.TargetFile,
			Symbols:     s.Symbols,
			Acceptance:  s.Acceptance,
			Description: s.Description,
			DependsOn:   s.DependsOn,
		}
	}
	return views
}

// PromptContext holds the codebase context for prompt building.
type PromptContext struct {
	FileCount     int
	SymbolCount   int
	Languages     []string        // detected languages
	FileTree      []string        // directory-grouped file listing
	TopSymbols    []*index.Symbol // ranked by PageRank
	RelevantEdges []string        // call graph edges relevant to the task
	RecentCommits []string        // last N commit onelines
	TaskBranch    string
	BaseBranch    string
	TestCmd       string
}

// BuildPromptContext constructs the full context from the index and git.
func BuildPromptContext(store *index.Store, root, taskBranch, baseBranch, testCmd, taskDescription string) PromptContext {
	fc, _ := store.FileCount()
	sc, _ := store.SymbolCount()
	topSymbols, _ := store.TopSymbols(30)

	ctx := PromptContext{
		FileCount:   fc,
		SymbolCount: sc,
		TopSymbols:  topSymbols,
		TaskBranch:  taskBranch,
		BaseBranch:  baseBranch,
		TestCmd:     testCmd,
	}

	// Languages from indexed files
	ctx.Languages = collectLanguages(store)

	// File tree
	ctx.FileTree = buildFileTree(store)

	// Recent git commits
	ctx.RecentCommits = recentCommits(root, 10)

	// Call graph edges relevant to the task — match task description words against symbol names
	ctx.RelevantEdges = relevantEdges(store, taskDescription)

	return ctx
}

func collectLanguages(store *index.Store) []string {
	files, _ := store.AllFiles()
	langSet := make(map[string]bool)
	for _, f := range files {
		if f.Language != "" {
			langSet[f.Language] = true
		}
	}
	var langs []string
	for l := range langSet {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return langs
}

func buildFileTree(store *index.Store) []string {
	files, _ := store.AllFiles()
	if len(files) > 200 {
		// Too many files — just show directory summary
		dirs := make(map[string]int)
		for _, f := range files {
			dir := f.Path
			if idx := strings.LastIndex(dir, "/"); idx > 0 {
				dir = dir[:idx]
			} else {
				dir = "."
			}
			dirs[dir]++
		}
		var lines []string
		for dir, count := range dirs {
			lines = append(lines, fmt.Sprintf("%s/ (%d files)", dir, count))
		}
		sort.Strings(lines)
		return lines
	}

	// Small repo — show all files grouped by directory
	dirFiles := make(map[string][]string)
	for _, f := range files {
		dir := "."
		if idx := strings.LastIndex(f.Path, "/"); idx > 0 {
			dir = f.Path[:idx]
		}
		dirFiles[dir] = append(dirFiles[dir], f.Path)
	}

	var lines []string
	var dirs []string
	for d := range dirFiles {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		for _, path := range dirFiles[dir] {
			lines = append(lines, path)
		}
	}
	return lines
}

func recentCommits(root string, n int) []string {
	out, err := execGit(root, "log", "--oneline", fmt.Sprintf("-%d", n))
	if err != nil {
		return nil
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			commits = append(commits, line)
		}
	}
	return commits
}

func relevantEdges(store *index.Store, taskDescription string) []string {
	// Find symbols mentioned in the task description
	words := strings.Fields(strings.ToLower(taskDescription))
	syms, _ := store.AllSymbols()

	var relevant []string
	for _, sym := range syms {
		nameL := strings.ToLower(sym.Name)
		for _, word := range words {
			if len(word) > 3 && strings.Contains(nameL, word) {
				// Found a relevant symbol — get its callers/callees
				callers, _ := store.Callers(sym.ID)
				callees, _ := store.Callees(sym.ID)
				for _, c := range callers {
					relevant = append(relevant, fmt.Sprintf("%s() → %s()", c.Name, sym.Name))
				}
				for _, c := range callees {
					relevant = append(relevant, fmt.Sprintf("%s() → %s()", sym.Name, c.Name))
				}
				break
			}
		}
		if len(relevant) > 20 {
			break // cap it
		}
	}
	return relevant
}

func execGit(root string, args ...string) (string, error) {
	cmd := execCommand("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	return string(out), err
}
