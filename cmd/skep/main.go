package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/llm"
)

const version = "0.1.0"

type command struct {
	name  string
	short string
	usage string
	long  string
	run   func([]string) error
}

var commands = []command{
	{
		name:  "init",
		short: "Set up skep in the current repo (guided)",
		usage: "skep init",
		long: `Interactive setup for a new repo. Asks you to configure:

  1. Workspace      Where to group related repos for cross-repo features
  2. Model          Execution model (sonnet, opus, haiku). Claude Code is
                    the only LLM backend in v0.1.0; Gemini/Codex return in
                    v0.2.0.
  3. Planning model Optional: different model for plan-gen than execution.
  4. Test command   Auto-detected from go.mod, package.json, Cargo.toml, etc.
  5. Auto-execute   Whether to auto-run small tasks.
  6. Cockpit config Writes ~/.tmux.conf.d/skep.conf (tmux keybinds, mouse,
                    scrollback, status-line hook). Idempotent; backs up any
                    existing file.

After setup, indexes the repo, registers it in the workspace, and
starts the daemon in a hidden tmux session named 'skep-<reponame>'.
Re-run 'skep init' anytime to reconfigure.`,
		run: cmdInit,
	},
	{
		name:  "index",
		short: "Build or query the code index ('index ask \"<query>\"' for search)",
		usage: "skep index [create|ask \"<query>\"]",
		long: `Index commands — build the code index, or search it.

Verbs:
  create     Build or incrementally refresh the index (default)
  ask <q>    Full-text search over symbol names, signatures, doc comments

Bare 'skep index' is equivalent to 'skep index create'.

Indexing walks all source files (respects .gitignore), parses with
tree-sitter (Go, TypeScript, JavaScript, Python, Kotlin, Java), and
extracts symbols into a SQLite FTS5 database.

First run is a full index. Subsequent runs are incremental — only re-parses
files changed since the last indexed git commit. Safe to run repeatedly.

Search uses FTS5 syntax:
  skep index ask "auth"
  skep index ask "auth*"               Prefix match
  skep index ask "handler OR service"  Boolean
  skep index ask "handler" --json | jq .

Data stored in .skep/ — add to .gitignore.
No git required (falls back to mtime-based change detection).
Uses git ls-files when available (10x faster on large repos).`,
		run: cmdIndex,
	},
	{
		name:  "status",
		short: "Show index, tasks, and config",
		usage: "skep status [--json]",
		long: `Shows the current state of the repo agent.

Example output:
  Repo: /home/user/code/backend
  Index: 125 files, 472 symbols
  Tasks: 3 total, 1 pending
  LLM: Claude Code (claude)

With --json:
  {"root":"/home/user/code/backend","files":125,"symbols":472,"tasks":3,"pending":1,"llm":"claude"}

Auto-refreshes stale index entries before responding. No daemon needed.`,
		run: cmdStatus,
	},
	{
		name:  "task",
		short: "Manage tasks (create, list, show, run, approve, reject, clarify, jump-pending, done, delete)",
		usage: "skep task <verb> [args]",
		long: `All task operations live under 'skep task'. Verbs:

  create <description> [--dry-run]  Create a task (--dry-run prints the plan
                                    without persisting anything)
  list [--all]                 List tasks in this repo, or across all repos
  show <id>                    Print full task detail, plan, and result file
  run <id> [--headless]        Execute a task interactively or detached
                               (--headless forks a subprocess, logs to
                               .skep/task-<id>.log, returns immediately)
  attach <id>                  Jump into a running task's tmux pane, or tail
                               its log if it's running headless
  approve <id>                 Move a pending task to approved
  reject <id>                  Mark a task as rejected
  clarify <id>                 Read answers from .skep/clarify/<id>.md and
                               re-run the classify + plan pipeline
  jump-pending                 Jump to the oldest task waiting on a
                               confirmation prompt (approval watchdog)
  done <id>                    Force-mark a task as done (no code to merge)
  delete <id> [--yes]          Hard-delete a task (refuses 'executing' tasks)

A verb is required. 'skep task "some description"' is NOT a shortcut —
it will error. Use 'skep task create "..."' to avoid accidentally creating
a task from a mistyped verb.

Task lifecycle:
  1. create    Four cheap dedup layers run (keyword, trigram, tf-idf, minhash)
               plus an optional LLM semantic check
  2. pipeline  Classify (Haiku) and plan-gen (Opus) run in parallel; classify
               decides size + confidence, plan-gen produces structured steps
  3. gate      small + auto-execute → approved
               large → pending (needs human approval)
               needs_clarification → pending_clarification (see 'clarify')
               reject → rejected
  4. run       LLM executes on a git branch, runs tests, commits
  5. done      Commits are in the base branch

Statuses:
  created, pending, pending_clarification, approved, queued, executing,
  done, failed, interrupted, rejected

Examples:
  skep task create "add GET /api/v2/charts/:id/export endpoint"
  skep task list --all
  skep task show 3
  skep task approve 3
  skep task clarify 3
  skep task run 3
  skep task delete 3 --yes

Shortcut: 'skep tasks' is a shortcut for 'skep task list'.`,
		run: cmdTask,
	},
	{
		name:  "tasks",
		short: "List tasks (shortcut for 'skep task list')",
		usage: "skep tasks [--all] [--json]",
		long: `Shortcut for 'skep task list'. Shows tasks with ID, name, status,
classification, and branch.

  skep tasks         Tasks in current repo
  skep tasks --all   Tasks across ALL registered repos

Example:
  $ skep tasks
  #1    logging-metrics-mcp            pending      [ambiguous]
  #2    fix-typo                       done         [small] → skep/task-2-fix-typo
  #3    input-validation               executing    [small]

Filter with jq:
  skep tasks --json | jq '.[] | select(.status == "pending")'`,
		run: cmdTaskList,
	},
	{
		name:  "config",
		short: "Get or set repo configuration",
		usage: "skep config <key> [value]",
		long: fmt.Sprintf(`Read:  skep config <key>           Show current value
Set:   skep config <key> <value>    Set a value

Keys:
  llm                  LLM CLI for task execution. Default: claude.
  llm-classify         LLM CLI for the classify + plan pipeline. Default: same as llm.
  llm-dedup            LLM CLI for the semantic-dedup escape hatch.
                       Default: same as llm-classify.
  model                Model for execution.
  model-classify       Classifier model. Default: claude-haiku-4-5-20251001 on claude.
  model-dedup          Dedup model. Default: claude-haiku-4-5-20251001 on claude.
  test-cmd             Shell command to run tests after task execution.
  auto-execute-small   Auto-approve small tasks. Default: false.
  tmux-layout          How 'skep task run' opens Claude inside tmux:
                         split-h  → vertical split, pane on the right (default)
                         split-v  → horizontal split, pane at the bottom
                         window   → new fullscreen tmux window
                         popup    → floating popup (tmux ≥3.2)
  approval_watchdog    Watch each executing task's tmux pane for "Do you want
                       to proceed?" prompts, surface via [!] and status line.
                       Default: true.
  approval_patterns    JSON array of Go regexps for the watchdog. Empty means
                       use the built-in defaults.

LLM presets:
%s

Examples:
  skep config llm claude
  skep config model opus                        Use Opus for execution
  skep config model-classify haiku              Haiku for classify (default)
  skep config test-cmd 'go test ./...'
  skep config auto-execute-small true`, formatPresets()),
		run: cmdConfig,
	},
	{
		name:  "workspace",
		short: "Manage the workspace (list, watch)",
		usage: "skep workspace [list|watch]",
		long: `A workspace is a directory with a .skep-workspace/ marker. It groups
related repos so they can delegate tasks to each other via MCP tools.

Verbs:
  list                     List all repos registered in this workspace (default)
  watch [--interval 2s]    Live dashboard of every task across every repo.
                           Pops up on 'Ctrl+b S' in the cockpit tmux config;
                           runnable standalone for scripting too.
                           Refreshes every 2s by default.

Examples:
  skep workspace                    Same as 'workspace list'
  skep workspace list --json
  skep workspace watch              Live dashboard
  skep workspace watch --once       Render once and exit (for scripts)
  skep workspace watch --interval 5s

Each listed repo can be targeted by cross-repo MCP tools
(create_remote_task, get_remote_task, approve_remote_task, wait_remote_task).`,
		run: cmdWorkspace,
	},
	{
		name:  "daemon",
		short: "Manage the agent daemon (start, stop, status)",
		usage: "skep daemon [start|stop|status]",
		long: `Manages the per-repo agent daemon. The daemon classifies new
tasks, executes approved tasks inside tmux panes, and routes cross-repo
task requests from peer repos over its Unix socket.

Verbs:
  start      Run the daemon in the foreground (default)
  stop       Stop the running daemon via its socket
  status     Report whether the daemon is running

Bare 'skep daemon' is equivalent to 'skep daemon start'.

IMPORTANT: ALWAYS start the daemon from inside a tmux session.

    tmux new -s skep
    cd your-repo
    skep daemon &

If you run 'skep daemon &' outside tmux, the daemon creates detached tmux
sessions for each task — you cannot see them and tasks appear to hang.

What the daemon does:
  - Classifies new tasks automatically via LLM CLI
  - Auto-executes small tasks (if auto-execute-small is true)
  - Spawns a new tmux pane for each approved task (one at a time per repo)
  - Listens on a socket for cross-repo task routing from other agents
  - Queues overflow tasks, dequeues when the active pane closes

Pane layout (inside tmux) controlled by config:
  split-h  → vertical split, new pane on the right (default)
  split-v  → horizontal split, new pane at the bottom
  window   → new tmux window (fullscreen)
  popup    → floating popup (tmux >= 3.2)
Change with: skep config tmux-layout <option>

When you DON'T need the daemon:
  skep status, tasks, workspace, index, task run — all work without it.

When you DO need the daemon:
  - Automatic task classification after 'skep task'
  - Automatic execution after 'skep task approve'
  - Cross-repo task routing between peer repos`,
		run: cmdDaemon,
	},
	{
		name:  "mcp",
		short: "Start or install the MCP server",
		usage: "skep mcp [serve|install]",
		long: `Model Context Protocol (MCP) integration for LLM CLIs like Claude Code.

Verbs:
  serve      Run the stdio MCP server (default — used by Claude Code).
  install    Write/merge the skep MCP server entry into ~/.claude/settings.json.
             Flags: --name <key> (default: skep), --force (overwrite existing).

Bare 'skep mcp' is equivalent to 'skep mcp serve'.

Install:
  $ skep mcp install
  Installed MCP server 'skep' in /home/you/.claude/settings.json
    command: /usr/local/bin/skep mcp
  Restart Claude Code to pick up the new server.

The installer preserves existing mcpServers entries and any unrelated
top-level keys. It uses the absolute path to the currently running skep
binary so the Claude settings keep working even if $PATH changes.

Tools exposed by the server:
  get_overview          Architecture, top symbols, active tasks, peer repos
  search_symbols        FTS5 search over symbol names, signatures, doc comments
  get_call_graph        Callers and callees of a symbol
  get_file_context      All symbols in a file with signatures
  list_tasks            Pending/active/completed tasks
  create_task           Create a task in this repo
  dedup_task            Check whether a proposed task duplicates an existing
                        one without creating it (4 cheap layers + optional LLM)
  create_remote_task    Create a task in a peer repo and block until classified
  get_remote_task       Read a delegated task's current state
  approve_remote_task   Approve a pending task on a peer repo
  wait_remote_task      Block until a peer task reaches a terminal state

The MCP server auto-refreshes the index before responding.
No daemon needed — it reads the SQLite index directly.`,
		run: cmdMCP,
	},
	{
		name:  "doctor",
		short: "Check environment health and dependencies",
		usage: "skep doctor [--json]",
		long: `Runs a health check of skep and its dependencies, surfacing anything
missing or misconfigured. Exit code is 0 when everything is healthy, 1 when
a critical dependency is missing (e.g. Claude Code CLI or tmux).

Checks:
  - skep version
  - operating system (Linux / macOS / WSL2 only)
  - claude CLI present and reachable
  - git present (warning if missing — indexing falls back to filesystem walk)
  - tmux present (critical — daemon needs it to spawn task panes)
  - universal-ctags present (warning — needed for Rust/Ruby/Swift/etc.)
  - .skep directory status for the current repo
  - workspace registry status

Use --json for machine-readable output suitable for CI / dashboards.`,
		run: cmdDoctor,
	},
	{
		name:  "cockpit",
		short: "Manage the tmux cockpit config (setup, reset, status)",
		usage: "skep cockpit [setup|reset|status]",
		long: `Writes a skep-owned tmux config snippet to ~/.tmux.conf.d/skep.conf.
You source it yourself from ~/.tmux.conf — skep never edits your
~/.tmux.conf directly.

Verbs:
  setup      Create ~/.tmux.conf.d/ (if missing) and write skep.conf.
             Any existing skep.conf is backed up as skep.conf.backup-<unix>.
  reset      Remove ~/.tmux.conf.d/skep.conf. Backups are left alone.
  status     Show whether skep.conf exists and whether ~/.tmux.conf
             already sources it. Supports --json.

Bare 'skep cockpit' is equivalent to 'skep cockpit status'.

One-time activation after 'skep cockpit setup' — add this to ~/.tmux.conf:
  source-file ~/.tmux.conf.d/skep.conf

The snippet wires up mouse scroll, vim pane navigation, task-window
jumps (prefix 1..9), popup launchers for 'skep workspace watch' and
'skep tasks', and a status-right driven by 'skep status --oneline'.`,
		run: cmdCockpit,
	},
	{
		name:  "completion",
		short: "Print a shell completion script (bash|zsh|fish)",
		usage: "skep completion <bash|zsh|fish>",
		long: `Prints a static shell completion script for skep to stdout. Source
it from your shell rc file to get tab-completion on verbs and common
flags.

Examples:
  # bash
  skep completion bash > /etc/bash_completion.d/skep
  # or user-local:
  skep completion bash > ~/.local/share/bash-completion/completions/skep

  # zsh — drop into an fpath directory
  skep completion zsh > "${fpath[1]}/_skep"

  # fish
  skep completion fish > ~/.config/fish/completions/skep.fish

The completion is static (verb + flag names). It does not introspect
.skep/index.db or the task table, so it won't complete task IDs.`,
		run: cmdCompletion,
	},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "skep: %v\n", err)
		if isUsageError(err) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	name := args[0]
	cmdArgs := args[1:]

	switch name {
	case "-h", "--help", "help":
		return cmdHelp(cmdArgs)
	case "-v", "--version", "version":
		// A dome-ish glyph, the version, nothing else.
		fmt.Printf("◠  skep %s\n", version)
		return nil
	}

	// Check --help BEFORE dispatching (so `skep init --help` shows help, not wizard)
	for _, a := range cmdArgs {
		if a == "-h" || a == "--help" {
			return cmdHelp([]string{name})
		}
		if a == "--" {
			break
		}
	}

	if dir := os.Getenv("SKEP_DIR"); dir != "" {
		os.Chdir(dir)
	}

	for _, c := range commands {
		if c.name == name {
			return c.run(cmdArgs)
		}
	}

	suggestion := suggestCommand(name)
	if suggestion != "" {
		return usageErrorf("unknown command '%s'. Did you mean '%s'?", name, suggestion)
	}
	return usageErrorf("unknown command '%s'. Run 'skep help' for usage.", name)
}

func printUsage() {
	fmt.Print(`◠  Skep — A persistent, always-warm code index per repo, served to
   Claude Code over MCP. Fewer tokens, grounded plans, cross-repo
   delegation built in.

Usage: skep <command> [args]

Quick start:
  skep init                          Set up skep (guided, one time)
  skep index ask "auth"              Search symbols (instant, no LLM)
  skep task create "add export api"  Create a task → classifies → plans
  skep task approve 1                Approve it
  skep task run 1                    Execute it (opens LLM CLI interactively)
  skep tasks                         List tasks (shortcut)

With the daemon (automatic execution):
  skep daemon &                      Start the agent in background
  skep task create "fix login bug"   Daemon classifies, opens tmux window
  skep task list --all               See tasks across all repos

Commands:
`)
	for _, c := range commands {
		fmt.Printf("  %-11s %s\n", c.name, c.short)
	}
	fmt.Print(`
Global options:
  -h, --help       Show help
  -v, --version    Show version

Per-command options:
  --json           Machine-readable JSON output (on commands that support it)
  --all            Show tasks across all repos (with 'skep tasks')

Environment:
  SKEP_DIR        Override working directory (like GIT_DIR)

Files:
  .skep/                     Per-repo data (index, config, tasks). Add to .gitignore.
  .skep-workspace/           Workspace marker + registry of all repos in the workspace.

Run 'skep help <command>' for details on any command.
`)
}

func cmdHelp(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	name := args[0]
	for _, c := range commands {
		if c.name == name {
			fmt.Printf("%s — %s\n\nUsage: %s\n", c.name, c.short, c.usage)
			if c.long != "" {
				fmt.Printf("\n%s\n", c.long)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown command: %s", name)
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...interface{}) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

func isUsageError(err error) bool {
	_, ok := err.(*usageError)
	return ok
}

// suggestCommand picks the closest command name for a typo using a combined
// score of common prefix + Levenshtein distance. Returns "" if nothing is
// close enough to justify a suggestion.
func suggestCommand(input string) string {
	type candidate struct {
		name  string
		score int
	}
	var best candidate
	for _, c := range commands {
		prefix := commonPrefix(input, c.name)
		dist := levenshtein(input, c.name)
		// Higher is better. Prefer long prefix matches; penalize edit distance.
		score := prefix*3 - dist
		if score > best.score {
			best.score = score
			best.name = c.name
		}
	}
	// Only suggest if we have at least 2 common prefix chars OR edit
	// distance ≤ 2. Keeps us from suggesting "task" for "xyz".
	if best.score >= 2 {
		return best.name
	}
	return ""
}

func commonPrefix(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// levenshtein computes the edit distance between a and b. O(len(a)*len(b))
// but command names are short so this is trivial.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = del
			if ins < curr[j] {
				curr[j] = ins
			}
			if sub < curr[j] {
				curr[j] = sub
			}
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func formatPresets() string {
	var b strings.Builder
	for name, p := range llm.Presets {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", name, p.DisplayName))
	}
	return b.String()
}
