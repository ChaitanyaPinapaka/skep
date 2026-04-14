---
title: Architecture
description: How Skep composes tree-sitter, SQLite FTS5, Unix sockets, tmux, and MCP into one tool.
---

Skep is a composition of standard primitives: tree-sitter indexing,
SQLite FTS5, Unix sockets, tmux panes, and MCP. Each piece is
well-established on its own; Skep's job is to wire them together
into a per-repo task orchestrator for Claude Code.

## The mental model

Three pieces. You only really need to understand one at a time.

### 1. The index

Every repo has a SQLite index built by tree-sitter. It records every
function, type, class, method, and doc comment in the repo, along
with their file locations and doc signatures. MCP tools query this
index directly — `search_symbols` for fuzzy lookup,
`get_file_context` for a file's symbol table, `get_call_graph` for
callers and callees. The index persists on disk and updates
incrementally as files change.

The daemon also computes a PageRank score over the symbol/file graph
at startup and after large index updates. The score is stored on each
symbol row and used to rank ties in `search_symbols` results — so a
symbol called from many places sorts above an equally-named unused
stub. See `internal/graph/rank.go` for the implementation.

### 2. The task lifecycle

Every task goes through the same states:

```text
created  →  classified  →  pending  →  approved  →  queued  →
executing  →  done  ─or─  interrupted  ─or─  failed  ─or─  rejected
```

Classification picks a scope (`small` / `large` / `ambiguous`) and a
plan. Small tasks auto-approve (when you opt in). Large tasks wait for
a human. Every task runs on its own git branch and records how many
tokens it cost you.

### 3. Cross-repo coordination

Cross-repo work uses four MCP tools that the sender's Claude session
calls against the peer's Skep daemon. (You may see this flow called
**"the waggle"** elsewhere in the docs — a bee-metaphor nickname for
exactly this MCP handoff.)

| Tool | Blocks? | Purpose |
|---|---|---|
| `create_remote_task` | yes, until peer classifies | hand a task to a peer, get back its plan |
| `approve_remote_task` | no | approve the peer's plan so it executes |
| `wait_remote_task` | yes, until peer finishes | block until the peer hits a terminal state |
| `get_remote_task` | no | non-blocking check on a peer task |

No polling loops. One call per step. The sender's Claude session never
drifts into the "I wonder if backend is done yet" ad-hoc behavior that
breaks multi-step plans.

```text
┌─────────────────────────────────────────────────────────┐
│                     your workspace                      │
│                                                         │
│   ┌──────────┐   ┌──────────┐   ┌──────────┐            │
│   │ backend  │   │  mobile  │   │  shared  │            │
│   │  .skep   │◄─►│  .skep   │◄─►│  .skep   │            │
│   │ ─ index  │MCP│ ─ index  │MCP│ ─ index  │            │
│   │ ─ tasks  │   │ ─ tasks  │   │ ─ tasks  │            │
│   │ ─ daemon │   │ ─ daemon │   │ ─ daemon │            │
│   └──────────┘   └──────────┘   └──────────┘            │
│        ▲               ▲               ▲               │
│        └───────────────┼───────────────┘               │
│                        │                               │
│                skep workspace watch                    │
│                   (your cockpit)                       │
└─────────────────────────────────────────────────────────┘
```

## The pieces

```
~/code/my-project/
├── .skep-workspace/
│   └── registry.json        ← workspace registry (which repos, which LLMs)
│
├── backend/
│   └── .skep/
│       ├── index.db         ← SQLite: files, symbols, edges, tasks, FTS5
│       ├── config.json      ← llm, model, test-cmd, tmux-layout
│       ├── daemon.pid       ← running daemon's PID
│       ├── agent.sock       ← Unix socket for inter-agent + CLI control
│       └── branches/        ← per-branch indexes for diffing
│
└── frontend/.skep/         ← same layout
```

Each repo gets its own daemon process:

```
skep daemon (per repo)
├── fsnotify watcher         ← re-indexes on file change
├── Unix socket listener     ← ask / create_task / list_tasks / stop
├── HTTP API sidecar         ← 127.0.0.1:<random>, bearer token auth
│                              (same verbs, for VS Code / webhook / remote surfaces)
└── task executor goroutine  ← serial queue, one task at a time
```

The HTTP sidecar listens on a random loopback port picked at daemon
startup and writes the URL + bearer token to `.skep/http.json`
(mode 0600). Clients that cannot speak the Unix-socket protocol —
notably editor extensions and webhook receivers — hit the HTTP
surface with the same JSON verbs as the socket listener. Everything
stays on `127.0.0.1`; there is no network exposure.

## How a task flows

```
skep task create "..."                (CLI, MCP, or peer daemon)
        │
        ▼
┌─────────────────────────────────────────────┐
│ dedup: FTS5 symbol match + in-flight scan   │
└─────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────┐
│ shell-out #1: classify + plan                │
│   claude -p "$(cat current-prompt.md)"      │
│   → stores classification + plan in tasks   │
└─────────────────────────────────────────────┘
        │
        ├── small + auto-execute → approved
        └── large / ambiguous → pending (human gate)
        │
        ▼
┌─────────────────────────────────────────────┐
│ git checkout -b skep/task-{id}-{slug}      │
│ materialize task_steps from plan_json       │
│ shell-out per step (not per task):          │
│   step 1 → LLM edits → commit → mark done   │
│   step 2 → LLM edits → commit → mark done   │
│   ... first-failure-stops-task, 1 retry/step│
└─────────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────────┐
│ post-execution                              │
│   re-index, diff branch vs main             │
│   write .skep/task-{id}-result.md          │
│   mark done / interrupted / failed          │
└─────────────────────────────────────────────┘
```

## Key packages

| Package          | Responsibility                              |
|------------------|---------------------------------------------|
| `internal/agent` | daemon lifecycle, socket listener, executor|
| `internal/index` | indexer, hasher, differ, SQLite store       |
| `internal/parser`| tree-sitter wrapping, symbol extraction     |
| `internal/graph` | call graph + PageRank                       |
| `internal/tasks` | create, dedup, classify, approve, delete    |
| `internal/mcp`   | stdio MCP server, cross-repo routing        |
| `internal/llm`   | shell-out wrapper, prompt file management   |
| `internal/registry` | workspace registry read/write            |

## Two-protocol stack

- **MCP** (vertical): how the interactive LLM session queries *this*
  repo's agent for symbols, call graphs, overviews, task status.
- **Unix socket JSON** (horizontal): how agents talk to *each other*
  across repos — `create_remote_task`, `wait_remote_task`,
  `approve_remote_task`, `list_tasks`.

See [Cross-repo delegation](/concepts/cross-repo/) and [MCP](/concepts/mcp/) for the
details of each layer.
