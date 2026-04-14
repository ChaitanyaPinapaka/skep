package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/agent"
	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/daemon"
	"github.com/ChaitanyaPinapaka/skep/internal/graph"
	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/llm"
	"github.com/ChaitanyaPinapaka/skep/internal/progress"
	"github.com/ChaitanyaPinapaka/skep/internal/registry"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
	"github.com/ChaitanyaPinapaka/skep/internal/tmuxutil"
)

// --- helpers ---

// repoRoot finds the repo root. Checks SKEP_DIR env first (like GIT_DIR),
// then git rev-parse --show-toplevel, then falls back to cwd.
func repoRoot() (string, error) {
	if dir := os.Getenv("SKEP_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return os.Getwd()
	}
	return strings.TrimSpace(string(out)), nil
}

func skepDir() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".skep"), nil
}

// openStore opens the index DB, ensuring freshness first.
func openStore() (*index.Store, string, string, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, "", "", err
	}
	rdir := filepath.Join(root, ".skep")
	dbPath := filepath.Join(rdir, "index.db")

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, "", "", fmt.Errorf("not initialized. Run 'skep index' first")
	}

	// On-demand freshness check
	index.EnsureFresh(root, rdir)

	store, err := index.OpenStore(dbPath)
	if err != nil {
		return nil, "", "", err
	}
	return store, root, rdir, nil
}

// jsonOrText outputs JSON if --json was passed, otherwise calls the text formatter.
func jsonOrText(args []string, data interface{}, textFn func()) {
	for _, a := range args {
		if a == "--json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(data)
			return
		}
	}
	textFn()
}

// --- commands ---

// cmdIndex dispatches `skep index [verb]`.
// Verbs: create (default — builds/refreshes the index), ask (search).
// Bare `skep index` builds the index.
func cmdIndex(args []string) error {
	if len(args) == 0 {
		return cmdIndexCreate(nil)
	}
	switch args[0] {
	case "create", "build", "refresh":
		return cmdIndexCreate(args[1:])
	case "ask", "search":
		return cmdAsk(args[1:])
	}
	// Unknown verb — treat as flags to create (preserves `skep index --json`).
	return cmdIndexCreate(args)
}

func cmdIndexCreate(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	rdir := filepath.Join(root, ".skep")
	dbPath := filepath.Join(rdir, "index.db")

	// Incremental: if index exists, use EnsureFresh (git-based diff).
	// First run: full index.
	var store *index.Store
	if _, err := os.Stat(dbPath); err == nil {
		// Existing index — incremental
		n, err := index.EnsureFresh(root, rdir)
		if err != nil {
			return err
		}
		store, err = index.OpenStore(dbPath)
		if err != nil {
			return err
		}
		_ = n
	} else {
		// First run — full index
		var err error
		store, err = index.FullIndexAndRecord(root, rdir)
		if err != nil {
			return err
		}
	}
	defer store.Close()

	// Compute PageRank so TopSymbols returns meaningful order
	prog := index.NewProgress()
	prog.Phase("computing ranks...")
	graph.ComputePageRank(store)
	prog.Phase("done")
	prog.Done()

	fc, _ := store.FileCount()
	sc, _ := store.SymbolCount()

	// Register in workspace registry (if workspace exists — set up via skep init)
	reg, _ := registry.LoadFrom(root)
	if reg != nil && reg.WorkspaceRoot() != "" {
		name := filepath.Base(root)
		cfg := config.Load(rdir)
		reg.Register(name, &registry.Agent{
			Path: root,
			LLM:  cfg.LLM,
		})
		reg.Save()
	}

	type indexResult struct {
		Root    string `json:"root"`
		Files   int    `json:"files"`
		Symbols int    `json:"symbols"`
	}
	result := indexResult{Root: root, Files: fc, Symbols: sc}

	jsonOrText(args, result, func() {
		fmt.Printf("Indexed %d files, %d symbols\n", fc, sc)
	})
	return nil
}

func cmdStatus(args []string) error {
	// --oneline: compact one-line summary for tmux status-right hooks.
	// Scans every registered workspace repo, not just the current one,
	// so a cockpit running in a neutral directory still sees everything.
	for _, a := range args {
		if a == "--oneline" {
			return cmdStatusOneline()
		}
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}
	rdir := filepath.Join(root, ".skep")

	type statusResult struct {
		Root               string `json:"root"`
		Files              int    `json:"files"`
		Symbols            int    `json:"symbols"`
		Tasks              int    `json:"tasks"`
		Pending            int    `json:"pending"`
		NeedsClarification int    `json:"needs_clarification"`
		Executing          int    `json:"executing"`
		LLM                string `json:"llm"`
	}

	result := statusResult{Root: root}

	dbPath := filepath.Join(rdir, "index.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		jsonOrText(args, result, func() {
			fmt.Printf("Repo: %s\nIndex: not initialized\n", root)
		})
		return nil
	}

	store, err := index.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	result.Files, _ = store.FileCount()
	result.Symbols, _ = store.SymbolCount()
	// Status queries use tasks.Status* constants via placeholders so a
	// rename never leaves a hard-coded literal behind.
	store.DB().QueryRow("SELECT count(*) FROM tasks").Scan(&result.Tasks)
	store.DB().QueryRow(
		"SELECT count(*) FROM tasks WHERE status IN (?,?,?)",
		tasks.StatusPending, tasks.StatusCreated, tasks.StatusClassified,
	).Scan(&result.Pending)
	store.DB().QueryRow(
		"SELECT count(*) FROM tasks WHERE status=?",
		tasks.StatusPendingClarification,
	).Scan(&result.NeedsClarification)
	store.DB().QueryRow(
		"SELECT count(*) FROM tasks WHERE status=?",
		tasks.StatusExecuting,
	).Scan(&result.Executing)

	cfg := config.Load(rdir)
	if name := llm.PresetName(cfg.LLMCmd()); name != "" {
		result.LLM = name
	} else {
		result.LLM = cfg.LLM
	}

	jsonOrText(args, result, func() {
		fmt.Printf("Repo: %s\n", root)
		fmt.Printf("Index: %d files, %d symbols\n", result.Files, result.Symbols)
		// Bold + yellow ANSI for the clarification count when non-zero so
		// it can't be missed at a glance. Plain text otherwise.
		clarifySegment := ""
		if result.NeedsClarification > 0 {
			clarifySegment = fmt.Sprintf(" \x1b[1;33m· %d needs clarification\x1b[0m", result.NeedsClarification)
		}
		execSegment := ""
		if result.Executing > 0 {
			execSegment = fmt.Sprintf(" · %d executing", result.Executing)
		}
		fmt.Printf("Tasks: %d total · %d pending%s%s\n", result.Tasks, result.Pending, clarifySegment, execSegment)
		if result.NeedsClarification > 0 {
			fmt.Printf("  → Run `skep task list` to find them, then `skep task clarify <id>` to answer.\n")
		}
		if name := llm.PresetName(cfg.LLMCmd()); name != "" {
			fmt.Printf("LLM: %s (%s)\n", llm.Presets[name].DisplayName, name)
		} else {
			fmt.Printf("LLM: %s\n", cfg.LLM)
		}
	})
	return nil
}

// cmdWorkspace dispatches `skep workspace <verb>`.
// Verbs: list (default), watch.
// Bare `skep workspace` lists repos in the workspace.
func cmdWorkspace(args []string) error {
	if len(args) == 0 {
		return cmdWorkspaceList(nil)
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "list", "ls":
		return cmdWorkspaceList(rest)
	case "watch":
		return cmdWorkspaceWatch(rest)
	}
	// Unknown verb — treat the whole arg list as flags for list.
	return cmdWorkspaceList(args)
}

func cmdWorkspaceList(args []string) error {
	reg, err := registry.Load()
	if err != nil {
		return err
	}

	jsonOrText(args, reg.Agents, func() {
		if len(reg.Agents) == 0 {
			fmt.Println("No repos registered in this workspace")
			return
		}
		for name, a := range reg.Agents {
			fmt.Printf("%-25s %s\n", name, a.Path)
		}
	})
	return nil
}

func cmdAsk(args []string) error {
	// Filter out flags
	var query string
	var flagArgs []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else if query == "" {
			query = a
		}
	}
	if query == "" {
		return usageErrorf("usage: skep index ask <query>")
	}

	store, _, _, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	syms, err := store.SearchSymbols(query, 20)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	jsonOrText(flagArgs, syms, func() {
		if len(syms) == 0 {
			fmt.Println("No matching symbols found")
			return
		}
		for _, s := range syms {
			fmt.Printf("%-12s %-30s %s:%d\n", s.Kind, s.Name, s.FilePath, s.Line)
			if s.Signature != "" {
				fmt.Printf("             %s\n", s.Signature)
			}
		}
	})
	return nil
}

// cmdTask dispatches `skep task <verb> ...`.
// Verbs: create, list, show, run, attach, approve, reject, done, delete.
// A verb is always required — no typo-prone bare-string shortcut.
func cmdTask(args []string) error {
	if len(args) == 0 {
		return usageErrorf("usage: skep task <create|list|show|run|attach|approve|reject|done|delete> [args]")
	}

	verb := args[0]
	rest := args[1:]
	switch verb {
	case "create":
		return cmdTaskCreate(rest)
	case "list", "ls":
		return cmdTaskList(rest)
	case "show":
		return cmdTaskShow(rest)
	case "run":
		return cmdRun(rest)
	case "attach":
		return cmdTaskAttach(rest)
	case "approve":
		return cmdApprove(rest)
	case "reject":
		return cmdReject(rest)
	case "done":
		return cmdDone(rest)
	case "delete", "rm":
		return cmdTaskDelete(rest)
	case "clarify":
		return cmdTaskClarify(rest)
	case "jump-pending":
		return cmdTaskJumpPending(rest)
	}

	// Unknown verb — refuse loudly rather than silently creating a task
	// named after the typo. Forces e.g. `skep task create "..."` for new tasks.
	return usageErrorf("unknown task verb '%s'. Run 'skep help task' for usage.", verb)
}

// cmdTaskJumpPending finds the oldest task across the workspace that
// the approval watchdog has flagged as waiting for input and jumps
// the tmux client to its window. Intended to be bound to `Ctrl+b !`
// via the cockpit config:
//
//	bind ! run-shell 'skep task jump-pending'
//
// Silent success: prints nothing on the happy path so the tmux status
// line flash doesn't double-print. Prints a brief "no tasks waiting"
// when there is nothing to jump to.
func cmdTaskJumpPending(args []string) error {
	_ = args
	reg, err := registry.Load()
	if err != nil || reg == nil {
		return fmt.Errorf("skep task jump-pending: no workspace registry")
	}
	// Scan every repo; pick the oldest NeedsInput task.
	var oldest *tasks.Task
	var oldestBranch string
	for _, a := range reg.Agents {
		if a == nil {
			continue
		}
		dbPath := filepath.Join(a.Path, ".skep", "index.db")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}
		store, err := index.OpenStore(dbPath)
		if err != nil {
			continue
		}
		rows, _ := tasks.ListByStatus(store, tasks.StatusExecuting)
		store.Close()
		for _, t := range rows {
			if !t.NeedsInput {
				continue
			}
			if oldest == nil || t.CreatedAt.Before(oldest.CreatedAt) {
				oldest = t
				oldestBranch = t.Branch
			}
		}
	}
	if oldest == nil {
		fmt.Println("No tasks waiting for approval.")
		return nil
	}
	// Find the tmux target for this task and switch the client.
	target := findTmuxTargetForTask(oldest.ID)
	if target == "" && oldestBranch != "" {
		// Fall back to branch-based lookup (same heuristic the daemon uses).
		target = findTmuxTargetByBranchName(oldestBranch)
	}
	if target == "" {
		fmt.Printf("Task #%d needs input but no tmux pane was found.\n", oldest.ID)
		return nil
	}
	if os.Getenv("TMUX") != "" {
		return exec.Command("tmux", "switch-client", "-t", target).Run()
	}
	session := strings.SplitN(target, ":", 2)[0]
	return exec.Command("tmux", "attach-session", "-t", session).Run()
}

// findTmuxTargetByBranchName forwards to internal/tmuxutil, which
// holds the single implementation used by both the CLI and the
// daemon's approval watchdog. Kept as a thin local wrapper so the
// call sites in this file read naturally.
func findTmuxTargetByBranchName(branch string) string {
	return tmuxutil.FindTargetByBranchName(branch)
}

// cmdStatusOneline emits a single compact line summarizing skep state
// across the entire workspace. Intended for tmux status-right feeds:
//
//	set -g status-right '#(skep status --oneline) %H:%M'
//
// Output shape:
//
//	◠ 2 exec · 1 pending · 🔔 #3 waiting approval
//
// When nothing is happening:
//
//	◠ idle
//
// We never exit non-zero here — a broken status command would turn
// the entire tmux bar into a visible error, which is worse than a
// missing update.
func cmdStatusOneline() error {
	reg, err := registry.Load()
	if err != nil || reg == nil {
		fmt.Println("◠ (no workspace)")
		return nil
	}
	var (
		exec       int
		pending    int
		clarify    int
		needsInput []int // task ids across all repos waiting on approval
	)
	for _, a := range reg.Agents {
		if a == nil {
			continue
		}
		dbPath := filepath.Join(a.Path, ".skep", "index.db")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}
		store, err := index.OpenStore(dbPath)
		if err != nil {
			continue
		}
		rows, _ := tasks.List(store)
		for _, t := range rows {
			switch t.Status {
			case tasks.StatusExecuting:
				exec++
				if t.NeedsInput {
					needsInput = append(needsInput, t.ID)
				}
			case tasks.StatusPending, tasks.StatusCreated, tasks.StatusClassified:
				pending++
			case tasks.StatusPendingClarification:
				clarify++
			}
		}
		store.Close()
	}

	if exec == 0 && pending == 0 && clarify == 0 && len(needsInput) == 0 {
		fmt.Println("◠ idle")
		return nil
	}
	var parts []string
	if exec > 0 {
		parts = append(parts, fmt.Sprintf("%d exec", exec))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if clarify > 0 {
		parts = append(parts, fmt.Sprintf("%d needs clarify", clarify))
	}
	if len(needsInput) > 0 {
		var ids []string
		for _, id := range needsInput {
			ids = append(ids, fmt.Sprintf("#%d", id))
		}
		parts = append(parts, fmt.Sprintf("🔔 %s waiting approval", strings.Join(ids, " ")))
	}
	fmt.Printf("◠ %s\n", strings.Join(parts, " · "))
	return nil
}

// cmdTaskClarify reads a task's clarify file, builds a clarified
// description, and re-runs the classify+plan pipeline. On success the
// task moves from pending_clarification → pending (or approved, if
// small + auto-execute enabled). The original clarify file is moved
// aside to .skep/clarify/archive/<id>-<timestamp>.md so a subsequent
// failure can still recover the user's answers.
func cmdTaskClarify(args []string) error {
	flagArgs, positional := splitFlagsAndPositional(args)
	if len(positional) == 0 {
		return usageErrorf("usage: skep task clarify <id>")
	}
	id, err := strconv.Atoi(positional[0])
	if err != nil {
		return usageErrorf("task clarify: invalid id %q", positional[0])
	}

	store, root, rdir, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	task, err := tasks.Get(store, id)
	if err != nil {
		return err
	}
	if task.Status != tasks.StatusPendingClarification {
		return fmt.Errorf("task #%d is %s, not pending_clarification — nothing to clarify", id, task.Status)
	}

	answers, err := tasks.ReadClarifyAnswers(rdir, id)
	if err != nil {
		return fmt.Errorf("read clarify answers: %w", err)
	}
	empty := true
	for _, qa := range answers {
		if strings.TrimSpace(qa.Answer) != "" {
			empty = false
			break
		}
	}
	if empty {
		return fmt.Errorf("no answers found in %s — fill in each `A:` line before running clarify",
			tasks.ClarifyFilePath(rdir, id))
	}

	clarified := tasks.BuildClarifiedDescription(task.Description, answers)
	// Re-run the pipeline with the clarified description. The task row
	// keeps its original description; the clarified version is only
	// used as pipeline input so the history stays auditable.
	transientTask := *task
	transientTask.Description = clarified

	cfg := config.Load(rdir)
	classifyTimeout := 180 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), classifyTimeout)
	defer cancel()

	fmt.Fprintf(os.Stderr, "◠  re-running classify + plan for task #%d with your clarifications...\n", id)
	sp := progress.NewSpinner("classify + plan")
	sp.Start()
	result, perr := tasks.ClassifyAndPlanCtx(ctx, store, root, cfg.ClassifyCmd(), cfg.PlanCmd(), &transientTask)
	sp.Stop()
	if perr != nil {
		return fmt.Errorf("re-classify: %w", perr)
	}
	if result == nil || result.Classification == "" {
		return fmt.Errorf("re-classify returned no result")
	}

	// Apply the new classification. Still-ambiguous tasks overwrite
	// the clarify file with the new question set so the user can
	// iterate; rejections surface the reason.
	task.Classification = result.Classification
	task.Plan = tasks.FormatPipelineResult(result)
	if len(result.Plan) > 0 {
		if raw, mErr := json.Marshal(result.Plan); mErr == nil {
			task.PlanStepsJSON = string(raw)
		}
	}
	switch {
	case result.Classification == "reject":
		task.Status = tasks.StatusRejected
		task.Result = result.RejectReason
	case result.NeedsClarification:
		task.Status = tasks.StatusPendingClarification
		if path, werr := tasks.WriteClarifyFile(rdir, id, task.Description, result.ClarifyingQuestions); werr == nil {
			task.Result = "still needs clarification: " + path
			fmt.Fprintf(os.Stderr, "\nTask #%d still needs clarification.\nUpdated questions: %s\n", id, path)
		}
	case result.Classification == "small" && cfg.AutoExecuteSmall:
		task.Status = tasks.StatusApproved
		task.Result = ""
	default:
		task.Status = tasks.StatusPending
		task.Result = ""
	}
	if err := tasks.Update(store, task); err != nil {
		return fmt.Errorf("update task: %w", err)
	}

	// Notify the daemon so it can pick up an auto-approved task immediately.
	if daemon.IsRunning(rdir) {
		daemon.Send(rdir, daemon.Request{Cmd: "notify_task", TaskID: task.ID})
	}

	jsonOrText(flagArgs, task, func() {
		fmt.Printf("#%d %s [%s]\n", task.ID, task.Name, task.Classification)
		if task.Plan != "" {
			fmt.Println(task.Plan)
		}
	})
	return nil
}

// mustCheckDedup runs the BM25 fast-path dedup check, swallowing errors
// (dedup is advisory, never gating). repoRoot is passed so the dedup
// layer can append to .skep/log/dedup.log.
func mustCheckDedup(store *index.Store, description, repoRoot string) *tasks.DedupResult {
	r, err := tasks.CheckDedupInRoot(store, description, repoRoot)
	if err != nil {
		return nil
	}
	return r
}

func cmdTaskCreate(args []string) error {
	var description string
	var flagArgs []string
	dryRun := false
	readStdin := false
	for _, a := range args {
		if a == "--dry-run" {
			dryRun = true
			continue
		}
		if a == "-" {
			readStdin = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else if description == "" {
			description = a
		}
	}
	if readStdin {
		if description != "" {
			return usageErrorf("task create: cannot pass both a positional description and '-'")
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		description = strings.TrimSpace(string(b))
		if description == "" {
			return fmt.Errorf("task create: stdin was empty")
		}
	}
	if description == "" {
		return usageErrorf("usage: skep task create <description|-> [--dry-run] [--json]")
	}

	store, root, rdir, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	cfg := config.Load(rdir)
	presetLabel := cfg.LLM
	if name := llm.PresetName(cfg.LLMCmd()); name != "" {
		presetLabel = llm.Presets[name].DisplayName
	}

	// Phase 1 — DEDUP (fast paths, no LLM).
	// Four cheap layers run in sequence, stopping at the first hit:
	//   keyword  — word-set overlap (exact/near-exact matches)
	//   trigram  — character 3-gram Jaccard (morphological variants)
	//   tfidf    — FTS5 BM25 ranking cosine (token-level semantics)
	//   minhash  — hashed n-gram LSH (paraphrase near-duplicates)
	// Each layer emits one line to .skep/log/dedup.log.
	fmt.Fprintf(os.Stderr, "◠  1/3  checking existing tasks (keyword → trigram → tfidf → minhash)...\n")
	if bm := mustCheckDedup(store, description, root); bm != nil && bm.IsDuplicate {
		jsonOrText(flagArgs, bm, func() {
			fmt.Printf("Duplicate (keyword): %s\n", bm.Reason)
		})
		return nil
	}
	if tg, _ := tasks.CheckDedupTrigram(store, description, root); tg != nil && tg.IsDuplicate {
		jsonOrText(flagArgs, tg, func() {
			fmt.Printf("Duplicate (trigram): %s\n", tg.Reason)
		})
		return nil
	}
	if tf, _ := tasks.CheckDedupTFIDF(store, description, root); tf != nil && tf.IsDuplicate {
		jsonOrText(flagArgs, tf, func() {
			fmt.Printf("Duplicate (tf-idf): %s\n", tf.Reason)
		})
		return nil
	}
	if mh, _ := tasks.CheckDedupMinHash(store, description, root); mh != nil && mh.IsDuplicate {
		jsonOrText(flagArgs, mh, func() {
			fmt.Printf("Duplicate (minhash): %s\n", mh.Reason)
		})
		return nil
	}

	// Phase 2 — DEDUP (semantic, LLM escape hatch).
	// BM25 missed it, but a paraphrase ("fix login bug" ≈ "repair auth issue")
	// might still be a duplicate. Ask the classify LLM with the top-5 active
	// tasks as candidates.
	//
	// Skip entirely when there are zero active tasks in the repo — nothing
	// to compare against, no reason to spend an LLM round trip.
	activeCount, _ := tasks.CountActive(store)
	if activeCount == 0 {
		fmt.Fprintf(os.Stderr, "◠  2/3  skipping semantic dedup (no existing tasks to compare against)\n")
	} else {
		fmt.Fprintf(os.Stderr, "◠  2/3  checking for semantic duplicates across %d existing task(s) via %s  (typical 5–20s)...\n", activeCount, presetLabel)
		sp := progress.NewSpinner(fmt.Sprintf("semantic dedup via %s", presetLabel))
		sp.Start()
		llmDup, _ := tasks.CheckDedupLLM(store, description, root, cfg.DedupCmd())
		sp.Stop()
		if llmDup != nil && llmDup.IsDuplicate {
			jsonOrText(flagArgs, llmDup, func() {
				fmt.Printf("Duplicate (semantic): %s\n", llmDup.Reason)
			})
			return nil
		}
	}

	// Dry-run short-circuit: run the classify+plan pipeline against a
	// transient Task object without inserting anything into the DB.
	// Useful for previewing what the system would plan before committing.
	if dryRun {
		transientTask := &tasks.Task{Description: description, Name: "(dry-run)"}
		sp := progress.NewSpinner(fmt.Sprintf("(dry-run) classify + plan via %s...", presetLabel))
		sp.Start()
		dryCtx, dryCancel := context.WithTimeout(context.Background(), 180*time.Second)
		result, err := tasks.ClassifyAndPlanCtx(dryCtx, store, root, cfg.ClassifyCmd(), cfg.PlanCmd(), transientTask)
		dryCancel()
		sp.Stop()
		if err != nil {
			return fmt.Errorf("dry-run classify+plan: %w", err)
		}
		if result == nil {
			fmt.Println("(dry-run) no LLM configured; nothing to show")
			return nil
		}
		fmt.Println("(dry-run) — nothing was persisted")
		fmt.Printf("Description: %s\n", description)
		fmt.Println(tasks.FormatPipelineResult(result))
		fmt.Println("Re-run without --dry-run to actually create and queue this task.")
		return nil
	}

	// Dedup passed — insert the task. tasks.Create still runs its own BM25
	// check internally for defense in depth, but by construction we already
	// know we're not a duplicate.
	task, dedup, err := tasks.Create(store, description, "", "")
	if err != nil {
		return err
	}
	if dedup != nil && dedup.IsDuplicate {
		// Race: another caller inserted a duplicate between our check and
		// this insert. Honor it.
		jsonOrText(flagArgs, dedup, func() {
			fmt.Printf("Duplicate: %s\n", dedup.Reason)
		})
		return nil
	}

	// Phase 3 — CLASSIFY + PLAN (parallel).
	// Two LLM calls run concurrently:
	//   classify  (Haiku / cheap model, ~3s, no MCP)
	//   plan      (Opus / highest, ~15s, MCP on by default)
	// If classify returns reject/ambiguous first, the plan call is
	// cancelled and its partial result discarded. Otherwise both
	// results are merged into a single PipelineResult.
	//
	// Bounded by a shared timeout so a stalled Claude CLI can't hang the
	// user's shell forever. Default 180s covers rate-limit retries and
	// tool-call loops. Override with SKEP_CLASSIFY_TIMEOUT.
	classifyTimeout := 180 * time.Second
	if v := os.Getenv("SKEP_CLASSIFY_TIMEOUT"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			classifyTimeout = time.Duration(sec) * time.Second
		}
	}
	fmt.Fprintf(os.Stderr, "◠  3/3  classify + plan in parallel via %s (classify: haiku, plan: %s) — typical 10–25s, timeout %s\n",
		presetLabel, presetLabel, classifyTimeout)

	classifyCtx, classifyCancel := context.WithTimeout(context.Background(), classifyTimeout)
	defer classifyCancel()

	spClassify := progress.NewSpinner(fmt.Sprintf("classify + plan via %s", presetLabel))
	spClassify.Start()
	result, pipelineErr := tasks.ClassifyAndPlanCtx(classifyCtx, store, root, cfg.ClassifyCmd(), cfg.PlanCmd(), task)
	spClassify.Stop()

	switch {
	case pipelineErr == nil && result != nil && result.Classification != "" && result.ClassifyErr == nil:
		task.Classification = result.Classification
		task.Plan = tasks.FormatPipelineResult(result)
		if len(result.Plan) > 0 {
			if raw, mErr := json.Marshal(result.Plan); mErr == nil {
				task.PlanStepsJSON = string(raw)
			}
		}
		switch {
		case result.Classification == "reject":
			task.Status = tasks.StatusRejected
			task.Result = result.RejectReason
		case result.NeedsClarification:
			task.Status = tasks.StatusPendingClarification
			if path, werr := tasks.WriteClarifyFile(rdir, task.ID, description, result.ClarifyingQuestions); werr == nil {
				task.Result = "needs clarification: " + path
				fmt.Fprintf(os.Stderr, "\nTask #%d needs clarification before planning.\nQuestions saved to %s\nAnswer with: skep task clarify %d\n",
					task.ID, path, task.ID)
			}
		case result.Classification == "small" && cfg.AutoExecuteSmall:
			task.Status = tasks.StatusApproved
		default:
			task.Status = tasks.StatusPending
		}
		tasks.Update(store, task)

	case result != nil && result.ClassifyErr != nil:
		// Classifier errored specifically (plan may still have succeeded).
		// Surface the classifier error; keep any plan we got as advisory.
		fmt.Fprintf(os.Stderr, "\nskep: classify failed: %v\n", result.ClassifyErr)
		task.Status = tasks.StatusPending
		task.Result = fmt.Sprintf("classify failed: %v", result.ClassifyErr)
		if len(result.Plan) > 0 {
			task.Plan = tasks.FormatPipelineResult(result)
		}
		tasks.Update(store, task)

	default:
		fmt.Fprintln(os.Stderr, "\nskep: classify+plan pipeline returned no result (is llm configured?)")
		task.Status = tasks.StatusPending
		task.Result = "pipeline returned no result"
		tasks.Update(store, task)
	}

	// Notify the daemon so it can pick up auto-approved tasks immediately
	if daemon.IsRunning(rdir) {
		daemon.Send(rdir, daemon.Request{Cmd: "notify_task", TaskID: task.ID})
	}

	jsonOrText(flagArgs, task, func() {
		fmt.Printf("#%d %s", task.ID, task.Name)
		if task.Classification != "" {
			fmt.Printf(" [%s]", task.Classification)
		}
		fmt.Println()
		if task.Plan != "" {
			fmt.Println(task.Plan)
		}
	})
	return nil
}

func cmdTaskList(args []string) error {
	// Check for --all flag
	allRepos := false
	var flagArgs []string
	for _, a := range args {
		if a == "--all" {
			allRepos = true
		} else {
			flagArgs = append(flagArgs, a)
		}
	}

	if allRepos {
		return cmdTasksAll(flagArgs)
	}

	store, root, _, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	taskList, err := tasks.List(store)
	if err != nil {
		return err
	}

	// Refresh status: flip "interrupted" tasks to "done" if their commits
	// are now merged into the base branch.
	refreshTaskStatuses(store, root, taskList)

	jsonOrText(flagArgs, taskList, func() {
		if len(taskList) == 0 {
			fmt.Println("No tasks")
			return
		}
		for _, t := range taskList {
			printTask(t)
		}
	})
	return nil
}

// refreshTaskStatuses checks each interrupted-with-branch task. If its commits
// are now in the base branch, flip status to "done" and persist.
func refreshTaskStatuses(store *index.Store, root string, taskList []*tasks.Task) {
	baseBranch, err := agent.GitCurrentBranchOr(root, "main")
	if err != nil || baseBranch == "" {
		baseBranch = "main"
	}
	for _, t := range taskList {
		if t.Status != tasks.StatusInterrupted || t.Branch == "" {
			continue
		}
		if agent.GitAllCommitsInBase(root, baseBranch, t.Branch) {
			t.Status = tasks.StatusDone
			tasks.Update(store, t)
		}
	}
}

// repoTask wraps a task with its repo name for --all output.
type repoTask struct {
	Repo string      `json:"repo"`
	Task *tasks.Task `json:"task"`
}

func cmdTasksAll(args []string) error {
	reg, err := registry.Load()
	if err != nil {
		return err
	}

	var all []repoTask
	for name, agent := range reg.Agents {
		dbPath := filepath.Join(agent.Path, ".skep", "index.db")
		store, err := index.OpenStore(dbPath)
		if err != nil {
			continue
		}
		taskList, err := tasks.List(store)
		store.Close()
		if err != nil {
			continue
		}
		for _, t := range taskList {
			all = append(all, repoTask{Repo: name, Task: t})
		}
	}

	jsonOrText(args, all, func() {
		if len(all) == 0 {
			fmt.Println("No tasks across any repos")
			return
		}
		for _, rt := range all {
			fmt.Printf("%-20s ", rt.Repo)
			printTask(rt.Task)
		}
	})
	return nil
}

func printTask(t *tasks.Task) {
	classStr := ""
	if t.Classification != "" {
		classStr = fmt.Sprintf(" [%s]", t.Classification)
	}
	branchStr := ""
	if t.Branch != "" {
		branchStr = fmt.Sprintf(" → %s", t.Branch)
	}
	tmuxStr := ""
	if t.Status == tasks.StatusExecuting || t.Status == tasks.StatusQueued {
		if target := findTmuxTargetForTask(t.ID); target != "" {
			tmuxStr = fmt.Sprintf(" @%s", target)
		}
	}
	fmt.Printf("#%-4d %-30s %-14s%s%s%s\n", t.ID, t.Name, t.Status, classStr, branchStr, tmuxStr)
	// For terminal statuses, print a short second line with the result
	// summary so users don't need `skep task show <id>` to see whether
	// a completed task actually did anything.
	if tasks.TerminalStatuses[t.Status] {
		if summary := tasks.ResultSummary(t); summary != "" {
			fmt.Printf("       └ %s\n", summary)
		}
	}
}

// findTmuxTargetForTask queries tmux for a pane/window belonging to the given task.
// Returns the target in "session:window.pane" format for use with
// `tmux switch-client -t <target>` or `tmux attach -t <target>`.
func findTmuxTargetForTask(taskID int) string {
	idStr := fmt.Sprintf("%d", taskID)
	prefix := "skep-" + idStr + "-"

	// Check all panes across all sessions. Look at both window_name (for new-window layout)
	// and pane_title (which we set via an escape sequence for split-h/split-v layouts).
	cmd := exec.Command("tmux", "list-panes", "-a",
		"-F", "#{session_name}:#{window_index}.#{pane_index}|#{window_name}|#{pane_title}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		target, windowName, paneTitle := parts[0], parts[1], parts[2]
		if strings.HasPrefix(windowName, prefix) || strings.HasPrefix(paneTitle, prefix) {
			return target
		}
	}

	// Fall back: check for a detached session named "skep-task-<id>" (tmux-available mode)
	sessionName := "skep-task-" + idStr
	if err := exec.Command("tmux", "has-session", "-t", sessionName).Run(); err == nil {
		return sessionName
	}

	return ""
}

func cmdApprove(args []string) error {
	flagArgs, positional := splitFlagsAndPositional(args)
	if len(positional) == 0 {
		return usageErrorf("usage: skep task approve <task-id>")
	}
	id, err := strconv.Atoi(positional[0])
	if err != nil {
		return usageErrorf("invalid task id: %s", positional[0])
	}

	store, _, rdir, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	if err := tasks.Approve(store, id); err != nil {
		return err
	}

	notified := false
	if daemon.IsRunning(rdir) {
		daemon.Send(rdir, daemon.Request{Cmd: "notify_task", TaskID: id})
		notified = true
	}

	jsonOrText(flagArgs, map[string]interface{}{
		"task_id":  id,
		"status":   tasks.StatusApproved,
		"notified": notified,
	}, func() {
		fmt.Printf("Task #%d approved\n", id)
		if notified {
			fmt.Println("daemon will execute it")
		} else {
			fmt.Println("run: skep task run " + positional[0])
		}
	})
	return nil
}

func cmdReject(args []string) error {
	flagArgs, positional := splitFlagsAndPositional(args)
	if len(positional) == 0 {
		return usageErrorf("usage: skep task reject <task-id>")
	}
	id, err := strconv.Atoi(positional[0])
	if err != nil {
		return usageErrorf("invalid task id: %s", positional[0])
	}

	store, _, _, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	if err := tasks.Reject(store, id); err != nil {
		return err
	}
	jsonOrText(flagArgs, map[string]interface{}{
		"task_id": id,
		"status":  tasks.StatusRejected,
	}, func() {
		fmt.Printf("Task #%d rejected\n", id)
	})
	return nil
}

func cmdDone(args []string) error {
	flagArgs, positional := splitFlagsAndPositional(args)
	if len(positional) == 0 {
		return usageErrorf("usage: skep task done <task-id>")
	}
	id, err := strconv.Atoi(positional[0])
	if err != nil {
		return usageErrorf("invalid task id: %s", positional[0])
	}

	store, _, _, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	task, err := tasks.Get(store, id)
	if err != nil {
		return err
	}
	task.Status = tasks.StatusDone
	if err := tasks.Update(store, task); err != nil {
		return err
	}
	jsonOrText(flagArgs, map[string]interface{}{
		"task_id": id,
		"status":  tasks.StatusDone,
	}, func() {
		fmt.Printf("Task #%d marked done\n", id)
	})
	return nil
}

// splitFlagsAndPositional separates `-foo` / `--foo` arguments from bare
// positional args. Does not attempt to parse flag values — any flag that
// takes a value must be written as `--flag=value`.
func splitFlagsAndPositional(args []string) (flags, positional []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return
}

func cmdTaskShow(args []string) error {
	var flagArgs []string
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else {
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		return usageErrorf("usage: skep task show <task-id>")
	}
	id, err := strconv.Atoi(positional[0])
	if err != nil {
		return usageErrorf("invalid task id: %s", positional[0])
	}

	store, root, _, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	task, err := tasks.Get(store, id)
	if err != nil {
		return err
	}

	// Try to load the per-task result file (written by task execution).
	resultPath := filepath.Join(root, ".skep", fmt.Sprintf("task-%d-result.md", id))
	resultFile := ""
	if b, err := os.ReadFile(resultPath); err == nil {
		resultFile = string(b)
	}

	type showResult struct {
		Task   *tasks.Task `json:"task"`
		Result string      `json:"result_file,omitempty"`
	}

	jsonOrText(flagArgs, showResult{Task: task, Result: resultFile}, func() {
		fmt.Printf("#%d %s\n", task.ID, task.Name)
		fmt.Printf("Status: %s\n", task.Status)
		if task.Classification != "" {
			fmt.Printf("Class:  %s\n", task.Classification)
		}
		if task.Branch != "" {
			fmt.Printf("Branch: %s\n", task.Branch)
		}
		if task.SourceRepo != "" {
			fmt.Printf("Source: %s (task #%d)\n", task.SourceRepo, task.SourceTaskID)
		}
		if task.TokensUsed > 0 {
			fmt.Printf("Tokens: %d (effective billed input)\n", task.TokensUsed)
		}
		fmt.Printf("\nDescription:\n  %s\n", task.Description)
		if task.Plan != "" {
			fmt.Printf("\n%s\n", task.Plan)
		} else if task.Classification == "" && task.Status == tasks.StatusPending {
			// Task is stuck in pending with no plan — most likely classifier error.
			// Surface that plainly so the user isn't confused about why there's no plan.
			fmt.Println("\n(no plan — the classifier has not run successfully yet)")
		}
		if task.Acceptance != "" {
			fmt.Printf("\nAcceptance:\n  %s\n", task.Acceptance)
		}
		if task.Result != "" {
			fmt.Printf("\n--- task.result ---\n%s\n", task.Result)
		}
		if resultFile != "" {
			fmt.Printf("\n--- result file (%s) ---\n%s\n", resultPath, resultFile)
		}
	})
	return nil
}

func cmdTaskDelete(args []string) error {
	var flagArgs []string
	var positional []string
	yes := false
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) == 0 {
		return usageErrorf("usage: skep task delete <task-id> [--yes]")
	}
	id, err := strconv.Atoi(positional[0])
	if err != nil {
		return usageErrorf("invalid task id: %s", positional[0])
	}

	store, _, _, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	task, err := tasks.Get(store, id)
	if err != nil {
		return err
	}

	// Confirm unless --yes. Stdin may be piped (automation) — in that case we
	// require --yes explicitly rather than hanging on a prompt.
	if !yes {
		fi, _ := os.Stdin.Stat()
		if (fi.Mode() & os.ModeCharDevice) == 0 {
			return fmt.Errorf("refusing to delete task #%d non-interactively without --yes", id)
		}
		fmt.Printf("Delete task #%d (%s, %s)? [y/N] ", task.ID, task.Name, task.Status)
		var resp string
		fmt.Scanln(&resp)
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("aborted")
			return nil
		}
	}

	if err := tasks.Delete(store, id); err != nil {
		return err
	}
	jsonOrText(flagArgs, map[string]interface{}{
		"task_id": id,
		"deleted": true,
	}, func() {
		fmt.Printf("Task #%d deleted\n", id)
	})

	// Best-effort: delete the git branch the task was working on. Task row is
	// already gone, so any failure here is non-fatal — just warn and continue.
	if task.Branch != "" {
		if root, err := repoRoot(); err == nil {
			verify := exec.Command("git", "rev-parse", "--verify", task.Branch)
			verify.Dir = root
			if verify.Run() == nil {
				del := exec.Command("git", "branch", "-D", task.Branch)
				del.Dir = root
				if err := del.Run(); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not delete branch %s: %v\n", task.Branch, err)
				} else {
					fmt.Fprintf(os.Stderr, "Deleted git branch %s\n", task.Branch)
				}
			}
		}
	}
	return nil
}

func cmdRun(args []string) error {
	// Parse flags:
	//   --inline    run directly without tmux wrapping (used internally when
	//               tmux spawns us as a child process)
	//   --headless  fork a detached subprocess, redirect its stdout/stderr to
	//               .skep/task-<id>.log, and return immediately. Nothing to
	//               attach to; user can tail the log or run 'skep task attach'.
	inline := false
	headless := false
	var taskArgs []string
	for _, a := range args {
		switch a {
		case "--inline":
			inline = true
		case "--headless":
			headless = true
		default:
			taskArgs = append(taskArgs, a)
		}
	}

	if len(taskArgs) == 0 {
		return usageErrorf("usage: skep task run <task-id> [--headless]")
	}
	id, err := strconv.Atoi(taskArgs[0])
	if err != nil {
		return usageErrorf("invalid task id: %s", taskArgs[0])
	}

	store, root, rdir, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	task, err := tasks.Get(store, id)
	if err != nil {
		return err
	}
	switch task.Status {
	case tasks.StatusApproved, tasks.StatusQueued, tasks.StatusCreated,
		tasks.StatusClassified, tasks.StatusPending, tasks.StatusFailed, tasks.StatusInterrupted:
		// Runnable — failed/interrupted are retries
	case tasks.StatusDone:
		fmt.Fprintf(os.Stderr, "Task #%d is done. Re-running will continue the same session.\n", id)
	case tasks.StatusRejected:
		return fmt.Errorf("task #%d was rejected", id)
	case tasks.StatusExecuting:
		return fmt.Errorf("task #%d is already executing", id)
	default:
		return fmt.Errorf("task #%d is %s, cannot run", id, task.Status)
	}

	// If --inline, run directly (we're already in the right terminal)
	if inline {
		cfg := config.Load(rdir)
		return agent.Execute(root, rdir, store, cfg, task)
	}

	// Headless: fork ourselves with --inline and redirect output to a log.
	// Daemon uses this path by default for small auto-executed tasks so
	// nothing pops up on screen.
	if headless {
		logPath := filepath.Join(rdir, fmt.Sprintf("task-%d.log", id))
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}

		exe, err := os.Executable()
		if err != nil {
			exe = "skep"
		}
		store.Close() // release DB before forking child with --inline

		cmd := exec.Command(exe, "task", "run", strconv.Itoa(id), "--inline")
		cmd.Dir = root
		cmd.Stdin = nil
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			logFile.Close()
			return fmt.Errorf("start headless task: %w", err)
		}
		// Detach from the child so it survives this process exiting.
		cmd.Process.Release()
		logFile.Close() // the child has its own handle via Stdout/Stderr
		fmt.Printf("Task #%d started headless (pid %d)\n", id, cmd.Process.Pid)
		fmt.Printf("  logs:   %s\n", logPath)
		fmt.Printf("  attach: skep task attach %d\n", id)
		return nil
	}

	// Otherwise: try to spawn in a tmux window. Falls back to current terminal.
	cfg := config.Load(rdir)
	store.Close() // release DB before spawning subprocess
	return daemon.SpawnTaskInTmux(id, task.Name, cfg.TmuxLayout)
}

// cmdTaskAttach connects the user to a running task: either switches to its
// tmux pane if it has one, or tails its headless log.
func cmdTaskAttach(args []string) error {
	if len(args) == 0 {
		return usageErrorf("usage: skep task attach <task-id>")
	}
	id, err := strconv.Atoi(args[0])
	if err != nil {
		return usageErrorf("invalid task id: %s", args[0])
	}

	// If the task has a tmux pane, jump into it.
	if target := findTmuxTargetForTask(id); target != "" {
		if os.Getenv("TMUX") != "" {
			// Already in tmux — switch client instead of attach.
			cmd := exec.Command("tmux", "switch-client", "-t", target)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		// Not in tmux — attach to the session that owns the pane.
		session := strings.SplitN(target, ":", 2)[0]
		cmd := exec.Command("tmux", "attach-session", "-t", session)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// No tmux target — fall back to tailing the headless log.
	root, err := repoRoot()
	if err != nil {
		return err
	}
	logPath := filepath.Join(root, ".skep", fmt.Sprintf("task-%d.log", id))
	if _, err := os.Stat(logPath); err != nil {
		return fmt.Errorf("task #%d has no tmux pane and no log at %s", id, logPath)
	}
	fmt.Fprintf(os.Stderr, "Tailing %s (Ctrl+C to exit)\n", logPath)
	cmd := exec.Command("tail", "-f", logPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func cmdConfig(args []string) error {
	if len(args) < 1 {
		return usageErrorf("usage: skep config <key> [value]\nRun 'skep help config' for available keys.")
	}

	rdir, err := skepDir()
	if err != nil {
		return err
	}

	cfg := config.Load(rdir)

	// `skep config list` — dump all values, one per line.
	if args[0] == "list" || args[0] == "ls" {
		flagArgs := args[1:]
		type configView struct {
			LLM              string `json:"llm"`
			LLMClassify      string `json:"llm_classify,omitempty"`
			LLMDedup         string `json:"llm_dedup,omitempty"`
			Model            string `json:"model,omitempty"`
			ModelClassify    string `json:"model_classify,omitempty"`
			ModelDedup       string `json:"model_dedup,omitempty"`
			TestCmd          string `json:"test_cmd,omitempty"`
			TmuxLayout       string `json:"tmux_layout"`
			AutoExecuteSmall bool   `json:"auto_execute_small"`
		}
		view := configView{
			LLM: cfg.LLM, LLMClassify: cfg.LLMClassify, LLMDedup: cfg.LLMDedup,
			Model: cfg.Model, ModelClassify: cfg.ModelClassify, ModelDedup: cfg.ModelDedup,
			TestCmd: cfg.TestCmd, TmuxLayout: cfg.TmuxLayout,
			AutoExecuteSmall: cfg.AutoExecuteSmall,
		}
		jsonOrText(flagArgs, view, func() {
			printConfigLine := func(k, v string) {
				if v == "" {
					v = "(unset)"
				}
				fmt.Printf("  %-20s %s\n", k, v)
			}
			fmt.Println("config (.skep/config.json):")
			printConfigLine("llm", cfg.LLM)
			printConfigLine("llm-classify", cfg.LLMClassify)
			printConfigLine("llm-dedup", cfg.LLMDedup)
			printConfigLine("model", cfg.Model)
			printConfigLine("model-classify", cfg.ModelClassify)
			printConfigLine("model-dedup", cfg.ModelDedup)
			printConfigLine("test-cmd", cfg.TestCmd)
			printConfigLine("tmux-layout", cfg.TmuxLayout)
			printConfigLine("auto-execute-small", fmt.Sprintf("%v", cfg.AutoExecuteSmall))
		})
		return nil
	}

	if len(args) == 1 {
		switch args[0] {
		case "llm":
			if name := llm.PresetName(cfg.LLMCmd()); name != "" {
				fmt.Printf("%s (%s)\n", name, llm.Presets[name].DisplayName)
			} else {
				fmt.Println(cfg.LLM)
			}
		case "llm-classify":
			v := cfg.LLMClassify
			if v == "" {
				v = cfg.LLM + " (same as llm)"
			}
			fmt.Println(v)
		case "tmux-layout":
			fmt.Println(cfg.TmuxLayout)
		case "model":
			v := cfg.Model
			if v == "" {
				v = "(default)"
			}
			fmt.Println(v)
		case "model-classify":
			v := cfg.ModelClassify
			if v == "" {
				v = cfg.Model
				if v == "" {
					v = "(default)"
				}
				v += " (same as model)"
			}
			fmt.Println(v)
		case "test-cmd":
			fmt.Println(cfg.TestCmd)
		case "auto-execute-small":
			fmt.Println(cfg.AutoExecuteSmall)
		default:
			return usageErrorf("unknown config key: %s\nkeys: llm, llm-classify, model, model-classify, test-cmd, auto-execute-small, tmux-layout", args[0])
		}
		return nil
	}

	key, value := args[0], args[1]
	switch key {
	case "llm":
		cfg.LLM = value
	case "llm-classify":
		cfg.LLMClassify = value
	case "tmux-layout":
		cfg.TmuxLayout = value
	case "model":
		cfg.Model = value
	case "model-classify":
		cfg.ModelClassify = value
	case "test-cmd":
		cfg.TestCmd = value
	case "auto-execute-small":
		cfg.AutoExecuteSmall = value == "true"
	default:
		return usageErrorf("unknown config key: %s", key)
	}

	if err := config.Save(rdir, cfg); err != nil {
		return err
	}

	if key == "llm" {
		if p, ok := llm.Presets[value]; ok {
			fmt.Printf("Set LLM to %s (%s)\n", p.DisplayName, p.Name)
			return nil
		}
	}
	fmt.Printf("Set %s = %s\n", key, value)
	return nil
}
