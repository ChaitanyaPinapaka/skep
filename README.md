# ◠  Skep

**A persistent, always-warm code index per repo, served to Claude Code over MCP. First response in seconds instead of minutes, grounded plans, and cross-repo delegation built in.**

Claude Code can already work across multiple repos — point it at a directory and it'll `Read` + `Grep` + `Glob` its way around. The problem is cold starts. Every session re-scans the same files, re-discovers the same symbols, and burns input tokens on context the LLM already had yesterday.

Skep runs a local daemon per repo that keeps a tree-sitter + SQLite FTS5 index of the code always warm. Claude Code queries the index through MCP (`search_symbols`, `get_file_context`, `get_call_graph`) instead of reading files cold. You feel it in three ways:

- **First response in seconds, not minutes.** Classification starts against a pre-built index, not a cold walk.
- **Plans reference real symbols.** Plan generation calls `search_symbols` before writing a file path, so it cannot reference a function that doesn't exist.
- **Lower input-token bills.** Measurable for sessions that would otherwise spend their first several turns on `Read` / `Grep` / `Glob` loops. Run the benchmark in `benchmarks/` for numbers on your own workload.

The index stays warm as long as the daemon is running. On top of the hot index, Skep adds a task queue with dedup and classification, approval gates for large changes, and cross-repo delegation that lets one repo's session hand structured work to another.

A **skep** is a dome of woven straw — the oldest design for a beehive. Each repo gets its own worker: its own index, its own task queue, its own git state. When a task spans repos, the workers coordinate through the hive — leaving signals the others can sense, waggling the path to their findings, returning what they learned. You watch from the entrance.

Single binary. Local-first. No cloud. No API keys — Skep doesn't call LLM APIs, it shells out to the CLI tool you already have. v0.1.0 supports Claude Code; Gemini CLI and Codex CLI return in v0.2.0.

> **A note on the metaphor.** Traditional straw skeps had a dark side: harvesting honey usually meant destroying the skep and killing the colony. That's not this skep. Nothing gets torn open to get the work done. Workers keep running between tasks. The comb keeps growing. You're the beekeeper, not the honey-thief.

## System requirements

| Requirement | Version | Why | Check |
|-------------|---------|-----|-------|
| **Operating system** | Linux, macOS, WSL2 | Unix sockets, process management | `uname -s` |
| **Claude Code CLI** | Latest | Task execution. Skep shells out to it. v0.1.0 is Claude-only. | `claude --version` |
| **tmux** | 3.2+ | Daemon spawns interactive Claude sessions in tmux panes. Gated by `skep doctor`. | `tmux -V` |

### Optional

| Requirement | Why | Check |
|-------------|-----|-------|
| **git** | Faster indexing (`git ls-files`), branch management for tasks | `git --version` |
| **universal-ctags** | Symbol extraction for languages without a shipped tree-sitter parser (Rust, Swift, Ruby, etc.) | `ctags --version` |

### Not required

- **No Go** — download a prebuilt binary (see Install)
- **No API keys** — Skep doesn't call LLM APIs. Claude Code handles auth.
- **No Docker** — single binary, runs natively
- **No database server** — uses embedded SQLite
- **No cloud account** — everything is local

## Install

### One line (Linux, macOS, WSL2)

```bash
curl -fsSL https://raw.githubusercontent.com/ChaitanyaPinapaka/skep/main/install.sh | sh
```

Detects your OS and architecture, downloads the binary, installs to `/usr/local/bin`.

### With tmux (recommended for daemon mode)

```bash
# Ubuntu/Debian/WSL2
sudo apt install tmux

# macOS
brew install tmux

# Then install skep
curl -fsSL https://raw.githubusercontent.com/ChaitanyaPinapaka/skep/main/install.sh | sh
```

**Recommended `~/.tmux.conf` for the cockpit.** Claude Code runs produce a
lot of output; tmux's default 2000-line scrollback and disabled mouse
scroll will fight you. Two lines fix both:

```tmux
set -g mouse on
set -g history-limit 50000
```

Reload without restarting: `tmux source-file ~/.tmux.conf`.

### Build from source

Requires Go 1.21+ and a C compiler (gcc or clang) for tree-sitter.

```bash
git clone https://github.com/ChaitanyaPinapaka/skep.git
cd skep
make build
sudo mv skep /usr/local/bin/
```

### Verify

```bash
skep --version     # skep 0.1.0
skep --help        # all commands with examples
```

## Quick start

```bash
cd your-project

# 1. Set up (guided — LLM, model, tests, workspace)
skep init

# 2. Search the index (no LLM, instant, free)
skep index ask "handler"
skep index ask "auth*"                    # prefix search
skep index ask "handler" --json | jq .    # machine-readable

# 3. Create a task (LLM classifies and plans)
skep task create "add health check endpoint at GET /health"

# 4. Review the plan
skep tasks                          # shortcut for 'skep task list'
skep task show 1                    # full detail, plan, result file

# 5. Approve and execute (opens interactive LLM with MCP tools)
skep task approve 1
skep task run 1                     # opens LLM CLI interactively
```

## How it works

```
skep index
  ├── Walks source files (git ls-files or filesystem fallback)
  ├── Parses with tree-sitter (Go, TypeScript, JavaScript, Python)
  ├── Extracts functions, types, classes, interfaces, methods
  ├── Stores in SQLite with FTS5 full-text search
  ├── Builds call graph edges between symbols
  └── Computes PageRank for symbol importance ranking

skep task create "add export endpoint"
  ├── Dedup check against queued/executing tasks
  ├── Classifies via LLM: small, large, or ambiguous
  ├── Generates step-by-step execution plan
  └── Gates: small → auto-execute, large → waits for approval

skep task run 1
  ├── Opens LLM CLI interactively (Claude Code TUI by default)
  ├── Prompt includes: task, plan, codebase context, git instructions
  ├── LLM creates branch, edits code, commits, runs tests
  ├── You can interact, guide, approve edits mid-task
  ├── On exit: re-indexes repo, diffs branch vs main
  ├── Stores session ID (skep task run 1 resumes via --resume)
  └── Routes cross-repo delegate steps via MCP to peer repos
```

## Shells out to Claude Code

Skep doesn't call LLM APIs directly. It shells out to the Claude Code
CLI you already have installed. Your Claude subscription handles auth.
Skep orchestrates.

```bash
skep config model opus                # Execution model (default: preset picks)
skep config model_classify haiku      # Classifier model (default: Haiku 4.5)
```

The classify + plan pipeline runs two calls in parallel: a cheap
Haiku decision and a more expensive plan against the execution model.
Both defaults are set during `skep init`; change them anytime with
`skep config`.

v0.1.0 supports Claude Code only. Gemini CLI and Codex CLI return in
v0.2.0 once they're tested end-to-end with the parallel pipeline and
the cockpit integration.

## Works with any language

skep indexes any project. Tree-sitter parsing extracts symbols from:

| Language | Extracted symbols |
|----------|-------------------|
| **Go** | functions, methods, structs, interfaces, types |
| **TypeScript/JavaScript** | functions, classes, methods, interfaces, types, arrow functions |
| **Python** | functions, classes, methods |
| **Rust, Java, Kotlin, Swift, Ruby, C/C++, C#, HCL, Protobuf, SQL** | file-level indexing (language detection, hashing) |

Tree-sitter parsers for additional languages can be added. Symbol extraction requires ~50 lines per language.

## Works with any repo

- **Git repos** — uses `git ls-files` for fast file discovery, `git diff` for incremental indexing
- **Non-git directories** — falls back to filesystem walking with `.gitignore` parsing
- **Monorepos** — index subdirectories independently; peer repos in the same workspace can delegate tasks via MCP
- **Any size** — parallel parsing, incremental indexing, SQLite FTS5 scales to 100k+ symbols

## The workspace cockpit

skep is designed around a single tmux session — your **cockpit** — that
gives you live visibility into every task across every repo. The daemons
run hidden; task panes pop up inside your cockpit when you approve work;
a dashboard pane shows you what's happening system-wide at a glance.

### The layout

```
┌───────────────────────────────────────────────┬──────────────────────┐
│ your shell — drive skep from here            │ skep workspace      │
│                                               │ watch  — live task   │
│ $ skep task create "add /health endpoint"    │ dashboard, refreshes │
│ $ skep task approve 3                        │ every 2 seconds,     │
│                                               │ shows all repos      │
│                                               │                      │
├───────────────────────────────────────────────┤ #3 backend           │
│ skep task pane (spawned when you approve)    │    executing [small] │
│                                               │ #4 mobile            │
│ Claude Code working on the task, visible      │    pending   [large] │
│ here in your own session — not hidden in      │                      │
│ the daemon's session                          │                      │
└───────────────────────────────────────────────┴──────────────────────┘
```

### Setting up the cockpit

```bash
# Step 1: Start the cockpit session
tmux new -s work

# Step 2: Start a hidden daemon for each repo you're working on.
#         The daemon runs in its own detached session, NOT in your cockpit.
#         Task panes will still land in your cockpit because the daemon
#         targets your attached session.
cd ~/code/backend  && tmux new-session -d -s skep-backend  'skep daemon'
cd ~/code/frontend && tmux new-session -d -s skep-frontend 'skep daemon'

# Step 3: Inside the cockpit, split a pane for the live task dashboard
tmux split-window -h 'skep workspace watch'

# Step 4: Drive skep from the main pane as usual
skep task create "add /health endpoint on backend and call it from frontend"
skep task approve 1
# → backend's daemon opens a Claude pane in your cockpit
# → dashboard shows both tasks updating live
```

### Debugging a daemon

Daemons are hidden by default — the only reason to attach to them is to
watch their logs or kill a stuck session. They each live in a session
named `skep-<reponame>`:

```bash
tmux attach -t skep-backend              # jump in
# Ctrl+B D to detach and go back to your cockpit
```

Or see which daemon is bound where:

```bash
skep daemon status                       # pid, socket, tmux pane
```

### Stopping a daemon

```bash
cd ~/code/backend && skep daemon stop    # graceful shutdown
```

### What the daemon does

- Classifies new tasks automatically when you call `skep task create`
- Auto-executes small tasks (if `auto-execute-small=true` in config)
- Spawns a new tmux pane in your attached session for each approved task
- Queues overflow tasks, dequeues when the active pane closes
- Listens on a Unix socket for cross-repo task routing

### When you don't need the daemon

All read commands — `skep index ask`, `skep status`, `skep tasks`,
`skep workspace`, `skep index` — work without a daemon. The daemon is
only required for automatic task classification, background
execution, and cross-repo task routing.

### Alternative: visible daemon pane

If you're debugging skep itself, a visible daemon pane is useful:

```bash
tmux split-window -h 'skep daemon'       # or just:
skep daemon &                            # backgrounds in current pane
```

### Pane layout

By default the daemon splits the current tmux window horizontally (new
pane on the right). Configure per repo with:

```bash
skep config tmux-layout split-h          # vertical split (default)
skep config tmux-layout split-v          # horizontal split
skep config tmux-layout window           # new window per task
skep config tmux-layout popup            # floating popup (tmux ≥ 3.2)
```

## Cross-repo agents

Multiple repos in the same workspace, each with its own agent, communicating
via Unix sockets. Cross-repo delegation is **explicit** — the classifier
emits `create_remote_task` steps in the plan when a task needs work in a
peer repo, and the executing LLM calls the MCP tool to route it.

```bash
# One-time setup: put both repos under a shared workspace
mkdir ~/code/my-project && cd ~/code/my-project
cd ~/code/my-project/backend  && skep init
cd ~/code/my-project/frontend && skep init

# Start a tmux session with one pane per repo
tmux new -s work

# In pane 1 (backend):
cd ~/code/my-project/backend
skep daemon &

# Split the window (Ctrl+B ") and in pane 2 (frontend):
cd ~/code/my-project/frontend
skep daemon &

# Submit a task in backend that affects frontend. The classifier sees
# "frontend" in <peer_repos>, emits a delegate step, and the LLM calls
# create_remote_task on the frontend daemon.
cd ~/code/my-project/backend
skep task create "add GET /api/v2/charts/:id/export endpoint and update the JS client"
skep task approve 1

# Monitor everything across the workspace
skep tasks --all
```

## MCP integration

skep exposes an [MCP](https://modelcontextprotocol.io/) server so LLM CLIs can query the index live during coding sessions.

One-shot install (writes/merges into `~/.claude/settings.json`, preserving any existing servers):

```bash
skep mcp install                  # installs under the key "skep"
skep mcp install --name backend   # install under a different key
skep mcp install --force          # overwrite an existing entry
```

Or hand-edit:

```json
// ~/.claude/settings.json
{
  "mcpServers": {
    "skep": { "command": "skep", "args": ["mcp"] }
  }
}
```

| Tool | Description |
|------|-------------|
| `get_overview` | Architecture, top symbols, active tasks, dependencies |
| `search_symbols` | FTS5 search over the entire codebase index |
| `get_call_graph` | Who calls this function? What does it call? |
| `get_file_context` | All symbols in a file without reading the file |
| `list_tasks` | Pending, active, completed tasks |
| `create_task` | Create a task in this repo |
| `create_remote_task` | Create a task in another repo via its daemon |

The MCP server reads the SQLite index directly. No daemon needed.

## Commands

| Command | Description |
|---------|-------------|
| `skep init` | Guided setup (models, tests, workspace, cockpit config) |
| `skep doctor` | Environment health check |
| `skep index [create]` | Build or refresh the index (incremental) |
| `skep index ask <query>` | Search symbols (no LLM, instant) |
| `skep task create <desc>` | Create a task (dedup → classify+plan) |
| `skep task list [--all]` | List tasks (current repo or all repos) |
| `skep tasks [--all]` | Shortcut for `task list` |
| `skep task show <id>` | Show task detail, plan, and result |
| `skep task run <id>` | Execute a task interactively (resumes if interrupted) |
| `skep task attach <id>` | Jump to a running task's tmux pane |
| `skep task approve <id>` | Approve a pending task |
| `skep task reject <id>` | Reject a task |
| `skep task clarify <id>` | Answer classifier questions to unblock an ambiguous task |
| `skep task jump-pending` | Jump to oldest task waiting on a confirmation prompt |
| `skep task done <id>` | Force-mark a task as done |
| `skep task delete <id> [--yes]` | Hard-delete a task |
| `skep status` | Show index, tasks, config |
| `skep status --oneline` | Compact one-line workspace summary (for tmux status-right) |
| `skep cockpit [setup\|reset\|status]` | Manage `~/.tmux.conf.d/skep.conf` |
| `skep config <key> [val]` | Get or set configuration |
| `skep config list` | Dump all config values |
| `skep workspace [list]` | List repos in the workspace |
| `skep daemon [start]` | Run the agent daemon in the foreground |
| `skep daemon stop` | Stop the daemon via its socket |
| `skep daemon status` | Daemon pid, socket, and tmux pane (if any) |
| `skep mcp` / `mcp serve` | Start MCP server on stdio |
| `skep mcp install` | Install the MCP server into `~/.claude/settings.json` |

All commands support `--json` for machine-readable output.
Run `skep help <command>` for detailed usage with examples.

## Configuration

Set during `skep init` or changed anytime. Stored in `.skep/config.json`.
See the [configuration reference](https://skep.sh/reference/config/)
for every key, default, and env-var override.

| Key | Default | Description |
|-----|---------|-------------|
| `llm` | `claude` | LLM CLI preset. v0.1.0 only accepts `claude`. |
| `model` | — (preset default) | Execution model (e.g. `opus`, `sonnet`). |
| `model_classify` | `claude-haiku-4-5-20251001` | Classifier model. Haiku-class by default. |
| `model_dedup` | `claude-haiku-4-5-20251001` | LLM semantic-dedup escape hatch model. |
| `test_cmd` | auto-detected | Command run after task execution. Gated by the out-of-repo trust store. |
| `tmux_layout` | `split-h` | How task panes open: split-h, split-v, window, popup |
| `auto_execute_small` | `false` | Auto-approve small tasks when the daemon is running. |
| `approval_watchdog` | `true` | Detect "Do you want to proceed?" prompts in background panes. |

```bash
skep config model opus                # Opus for execution (plan-gen + executor)
skep config test-cmd 'go test ./...'
skep config tmux-layout window
skep config auto-execute-small true
```

## Workspaces

A workspace groups related repos for cross-repo features. Created during `skep init`.

```
~/code/my-project/                       ← workspace root
  ├── .skep-workspace/
  │   └── registry.json                  ← all repos in this workspace
  ├── backend/.skep/                    ← repo index + config
  ├── frontend/.skep/                   ← repo index + config
  └── mobile/.skep/                     ← repo index + config
```

skep walks up from the current directory to find `.skep-workspace/`, like git finds `.git/`.

## Files

```
.skep-workspace/
  └── registry.json              # Workspace registry (all repos)

your-repo/.skep/                # Add to .gitignore (auto-added by init)
  ├── config.json                # Per-repo configuration
  ├── index.db                   # SQLite: files, symbols, edges, tasks, FTS5
  └── branches/                  # Branch-specific indexes (for task diffing)
```

## Design principles

1. **Reactive, not proactive.** Agents sit idle until work arrives. Zero cost at rest.
2. **Shell out, don't integrate.** LLM CLIs handle auth, context, file editing. skep orchestrates.
3. **Index is the brain.** Dedup, classification, and cross-repo propagation all flow from the structural index.
4. **Serial within, parallel across.** One task at a time per repo. Multiple repos work simultaneously.
5. **Human gates for big changes.** Small tasks auto-execute. Large tasks wait for approval.
6. **Contracts, not diffs.** Cross-repo communication uses structural contracts (functions, types, routes).

## License

MIT
