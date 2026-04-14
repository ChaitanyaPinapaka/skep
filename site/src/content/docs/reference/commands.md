---
title: Command reference
description: Every Skep command. Run skep help <command> for detailed usage and examples from the binary itself.
---

Every Skep command. Run `skep help <command>` for detailed usage
and examples from the binary itself.

All commands accept `--json` for machine-readable output — including
write verbs (`task create`, `task approve`, `task reject`, `task clarify`,
`task delete`).

## Setup and status

| Command | Description |
|---------|-------------|
| `skep init` | Guided setup — picks LLM, model, test command, workspace. Indexes the repo, registers it, and offers to write the cockpit tmux config. |
| `skep doctor` | Environment health check: binary, LLM CLI, tmux, git, workspace, daemon. |
| `skep status` | Index freshness, task counts (including clarification-pending), daemon status for the current repo. |
| `skep status --oneline` | Compact single-line summary across the whole workspace (`◠ 2 exec · 1 pending · 🔔 #5 waiting approval`). Intended for `tmux status-right` hooks; the cockpit config wires this automatically. |
| `skep cockpit setup` | Write `~/.tmux.conf.d/skep.conf` with the full set of cockpit keybindings, mouse, scrollback, and status-line config. |
| `skep cockpit status` | Report whether `skep.conf` is installed and whether your `~/.tmux.conf` sources it. |
| `skep cockpit reset` | Remove `~/.tmux.conf.d/skep.conf`. |
| `skep --version` | Print binary version. |
| `skep help <cmd>` | Detailed help with examples for any subcommand. |

## Index

| Command | Description |
|---------|-------------|
| `skep index` | Build or refresh the index (incremental). |
| `skep index create` | Explicit form of `skep index`. |
| `skep index ask <query>` | FTS5 search over symbols. |

## Tasks

| Command | Description |
|---------|-------------|
| `skep task create <desc> [--dry-run]` | Create a task. Runs four cheap dedup layers (keyword → trigram → tf-idf → minhash) plus an optional Haiku-class LLM semantic check, then fires classify (Haiku) and plan-generation (Opus / highest model) **in parallel**. `--dry-run` prints the rendered plan without persisting anything. Pass `-` as the description to read it from stdin (`echo "fix login" \| skep task create -`). |
| `skep task run <id> [--headless]` | Execute a task. If the task has a valid Claude session id, resumes it via `claude --resume`; otherwise starts a fresh run. `--headless` forks a subprocess, redirects output to `.skep/task-<id>.log`, returns immediately. |
| `skep task list` | List tasks in the current repo. Terminal-state rows include a short one-line result summary. |
| `skep task list --all` | List tasks across the entire workspace. |
| `skep tasks [--all]` | Shortcut for `task list`. |
| `skep task show <id>` | Full task detail: classification, plan, per-step execution state (✓/✗/→/· glyphs with verb, target file, commit SHA, duration, retry count), result file, tokens used, tools used. |
| `skep task attach <id>` | Jump to the task's tmux pane (`tmux switch-client`) or tail its headless log if it has no pane. |
| `skep task approve <id>` | Approve a large or ambiguous task. |
| `skep task reject <id>` | Reject a task. |
| `skep task clarify <id>` | Read answers from `.skep/clarify/<id>.md`, merge them into the task description, and re-run the classify + plan pipeline. |
| `skep task jump-pending` | Find the oldest task across the workspace waiting on approval (flagged by the watchdog) and `tmux switch-client` to its window. Bound to `Ctrl+b !` in the cockpit. |
| `skep task done <id>` | Force-mark a task as done (no merge needed). |
| `skep task delete <id> [--yes]` | Hard-delete a task row. |

## Workspace

| Command | Description |
|---------|-------------|
| `skep workspace` | List repos registered in the current workspace. |
| `skep workspace list` | Explicit form. |
| `skep workspace watch [--interval <dur>] [--once]` | Live dashboard of tasks across every repo. `--interval` sets the refresh cadence (default `2s`); `--once` renders a single frame and exits. |

## Daemon

| Command | Description |
|---------|-------------|
| `skep daemon` | Run the foreground daemon (equivalent to `daemon start`). |
| `skep daemon start` | Explicit form. |
| `skep daemon stop` | Stop the daemon via its Unix socket. |
| `skep daemon status` | PID, socket path, and the tmux pane it's targeting (if any). |

## Config

| Command | Description |
|---------|-------------|
| `skep config <key>` | Read a config value. |
| `skep config <key> <value>` | Set a config value. |
| `skep config list` | Dump all config values. |

See the [Configuration reference](/reference/config/) for every key.

## MCP

| Command | Description |
|---------|-------------|
| `skep mcp` | Run the stdio MCP server (what Claude Code invokes). |
| `skep mcp serve` | Explicit form. |
| `skep mcp install` | Write/merge the server entry into `~/.claude/settings.json`. |
| `skep mcp install --name <key>` | Install under a custom key. |
| `skep mcp install --force` | Overwrite an existing entry. |

## Shell completions

| Command | Description |
|---------|-------------|
| `skep completion bash` | Emit bash completion script. Source the output from your `~/.bashrc` or `/etc/bash_completion.d/`. |
| `skep completion zsh` | Emit zsh completion script. Place it in a directory on your `$fpath`. |
| `skep completion fish` | Emit fish completion script. Place it in `~/.config/fish/completions/skep.fish`. |

## Global flags

| Flag | Description |
|------|-------------|
| `--json` | Machine-readable output. Supported on every read **and** write command — reads emit the requested data; writes emit the mutated row. |
| `--help` | Print usage for a command. |
| `-v` / `--version` | Print binary version. |

`skep mcp --repo <path>` accepts a specific repo path for the MCP
server subcommand. Other commands infer the repo from `cwd` by walking
up to the nearest `.skep-workspace/` marker. To run against a
different repo from anywhere, set `SKEP_DIR=/path/to/repo/.skep` for
that invocation.
