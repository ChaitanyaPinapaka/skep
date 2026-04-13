---
title: FAQ
description: Why tmux, does it work on Windows, and other common questions about running Skep.
---

## Why tmux?

Task execution runs an interactive LLM TUI (Claude Code) that edits
files and expects a real PTY. Skep uses tmux to attach a new pane to
your existing session from a background daemon, giving every repo's
session a persistent home and letting the cockpit pull live status
from all of them into one status line. If you'd rather not use tmux,
run the daemon in the foreground and skip the cockpit — every read
command (`skep index ask`, `skep task list`, `skep status`) works
without it.

## Does it work on Windows?

On WSL2, yes. On native Windows, no — Skep uses Unix domain sockets
for inter-agent communication and fsnotify for file watching, both
of which are much happier in a Linux userspace. WSL2 with Ubuntu is
the supported path.

## Can I use Gemini CLI or Codex CLI instead of Claude Code?

Not in the current release. Skep ships with Claude Code as the only
wired backend; Gemini CLI and Codex CLI are planned once both are
tested end-to-end with the parallel classify + plan pipeline, the
approval watchdog, and cross-repo delegation. See
[Changelog](/changelog/) for the current state.

## What does `skep task create` do?

`skep task create "<description>"` writes a task row to the repo's
local database and runs it through the skep task lifecycle:

1. **Dedup** — searches the symbol index and the existing task queue
   for matching work. If the task is already implemented or already
   queued, skep reports that and exits.
2. **Classify** — shells out to the configured LLM CLI to produce a
   scope label (`small` / `large` / `ambiguous`) and a structured plan.
3. **Persist** — writes the plan to `.skep/task-<id>-plan.md` and the
   task metadata to `.skep/index.db`. Both remain readable a week
   later, with full history.
4. **Gate** — small tasks auto-approve when `auto-execute-small` is on;
   larger tasks wait for `skep task approve <id>`.
5. **Execute** — creates a dedicated git branch `skep/task-<id>-<slug>`,
   hands the plan to the LLM CLI, and records the result.
6. **Delegate** — if the plan contains cross-repo steps, skep routes
   them to peer daemons via MCP.

You can run the underlying `claude` session yourself at any point —
`skep task create` adds the structure around it.

## Is my code sent anywhere?

Only to the LLM CLI you configured, and only when you explicitly run
a task. Skep itself makes zero network calls. It stores everything
in `.skep/index.db` on your disk. It has no telemetry, no accounts,
no cloud.

## How do I stop the daemon?

```bash
cd your-repo
skep daemon stop
```

Or kill the process by PID from `.skep/daemon.pid`. The daemon
handles `SIGTERM` gracefully — it drains the current task before
exiting. `SIGKILL` is safe too, just less clean; the next command
will clean up the PID file.

## Why can't I approve a cross-repo task from the sender pane?

Deliberate design. Large tasks need a human review **on the repo
they run in**, not from the repo that delegated them. The reviewer
closest to the affected code should see the plan. The sender pane
prints the peer task id and a hint; you switch tabs in your cockpit,
run `skep task show <id>` against the peer, and approve there.

## How do I benchmark it?

See [Benchmarking Skep](/advanced/benchmarking/). Short version:
benchmarking is a maintainer tool, not a daily CLI verb. Every task
already records `tokens_used` automatically; the dedicated A/B harness
in `benchmarks/` is for producing reproducible blog-post numbers.

## Does Skep auto-merge to main?

No. Never. Skep creates isolated branches (`skep/task-<id>-<slug>`)
and commits to them. The human reviews and merges. This is
non-negotiable — automated merges to `main` are outside the tool's
scope.

## Can I use it without a daemon?

Yes for read commands: `skep index ask`, `skep status`, `skep tasks`,
`skep workspace`, `skep index`. These all read the SQLite index
directly. You need the daemon for automatic classification,
background execution, and cross-repo routing.

## What happens if indexing fails on a file?

Skep logs a warning to the daemon log and continues. One file with
an unsupported language or a parse error doesn't fail the whole
index. The next incremental run will retry it. You can also inspect
`.skep/index.db` directly with any SQLite browser if you want to
see what got indexed.

## Can I reindex from scratch?

```bash
rm .skep/index.db
skep index
```

No workspace state is lost — the registry and config live in
separate files.

## Does it support monorepos?

Yes. Run `skep init` inside each subdirectory you want indexed
independently and point them at the same workspace root. Each
subdirectory gets its own `.skep/`, its own daemon, and its own
entry in the workspace registry. Cross-repo delegation works between
them exactly as it does between separate checkouts.

## Can I write a plugin / custom MCP tool?

Plugin API isn't designed yet. For now the MCP tool set is fixed at
what `internal/mcp/server.go` exposes. If you have a specific tool
you want, file an issue — most new tools are a 50-line addition.

## How do I upgrade?

Re-run the install script:

```bash
curl -fsSL https://raw.githubusercontent.com/ChaitanyaPinapaka/skep/main/install.sh | sh
```

Or `go install github.com/ChaitanyaPinapaka/skep/cmd/skep@latest`
if you built from source. Indexes and configs are forward-compatible
across minor versions; Skep will re-index automatically if the
schema has changed.
