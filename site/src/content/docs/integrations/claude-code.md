---
title: "Claude Code"
description: "The Skep LLM backend — MCP integration, session resumption, tmux TUI mode."
---

Claude Code is the LLM backend Skep currently ships with (see
[Changelog](/changelog/)). This page describes how Skep integrates
with it.

## What Skep uses from Claude Code

- **MCP client.** Skep's cross-repo delegation (`create_remote_task`,
  `wait_remote_task`, `approve_remote_task`, `get_remote_task`) is
  routed through Claude Code's Model Context Protocol client.
  The executing session calls these tools mid-conversation to hand
  work to peer repos.
- **Session resumption.** Claude Code records every interactive
  session as a JSONL transcript with a stable UUID. Skep stores that
  UUID on the task row, so an interrupted task resumes via
  `claude --resume <uuid>` without rebuilding context or replaying
  the plan.
- **Interactive TUI.** A Claude pane spawned in tmux is still an
  interactive terminal. You can read what it's doing, intervene, answer
  prompts, hit Ctrl-C, edit a file by hand and ask it to keep going.
  Claude is not a black box.

## Install

Claude Code is distributed by Anthropic — see
[docs.claude.com/en/docs/claude-code](https://docs.claude.com/en/docs/claude-code).
Auth lives entirely inside Claude Code; Skep does not handle
credentials. There are no environment variables for Skep to set.

## Configure Skep to use Claude

```bash
skep config llm claude       # default; you may not need to run this
skep config model sonnet     # or opus, haiku
skep config list
```

Use a smarter model for planning and a faster model for execution:

```bash
skep config model-classify opus
skep config model sonnet
```

## What Skep injects

The `claude` preset wraps the user's prompt with a few flags depending
on which lifecycle stage Skep is in:

- **Interactive execution** (`skep task run`) —
  `claude --append-system-prompt "<skep system prompt>" "<plan prompt>"`.
  Skep prepends a system prompt that explains the task lifecycle, the
  branch naming convention, and which MCP tools are available.
- **Daemon execution** (auto-execute small tasks) —
  `claude -p --dangerously-skip-permissions --allowedTools "Edit,Write,Bash" "<prompt>"`.
  Non-interactive, no permission prompts, restricted tool surface.
- **Classification** — `claude -p "<classifier prompt>"`. No tools, no
  system prompt envelope, just a one-shot LLM call that returns JSON.

## MCP integration specifics

When the daemon spawns a Claude pane it writes a one-shot
`.skep/mcp.json` that registers the Skep stdio MCP server as a tool
Claude can call:

```json
{
  "mcpServers": {
    "skep": { "command": "skep", "args": ["mcp"] }
  }
}
```

Claude is invoked with `--mcp-config .skep/mcp.json`. The MCP lifecycle
is per-session, so every task run re-registers the server cleanly. You
do not need to run `skep mcp install` for task execution to work — the
global install only matters for ad-hoc Claude Code sessions outside
Skep.

## Session resumption

After every task run, Skep records the Claude session UUID on the task
row. If you re-run an interrupted task:

```bash
skep task run 7
```

Skep finds the existing session UUID and invokes Claude with
`--resume <uuid>` instead of starting fresh. The plan is not re-sent;
the conversation picks up where it left off. This is how mid-task
interruptions (closed tmux pane, daemon restart, Ctrl-C) recover
without losing work.

## Tmux behavior

By default the daemon splits the current tmux window horizontally and
runs `claude` in the new pane with the task prompt piped in. Other
layouts are configurable:

```bash
skep config tmux-layout split-h     # vertical split, pane on the right (default)
skep config tmux-layout split-v     # horizontal split, pane on the bottom
skep config tmux-layout window      # new fullscreen tmux window
skep config tmux-layout popup       # floating popup (tmux ≥ 3.2)
```

The daemon must be started from inside a tmux session for any of these
layouts to work — see
[Cockpit setup](/getting-started/cockpit/) for the canonical layout.

## Troubleshooting

If a task starts but Claude does nothing, or the pane never spawns,
start with [Troubleshooting](/advanced/troubleshooting/).
