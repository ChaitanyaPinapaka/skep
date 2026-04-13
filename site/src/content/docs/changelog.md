---
title: Changelog
description: Release notes for Skep. Version-specific details (supported LLM backends, state machine changes, breaking config) live here.
---

This page is the source of truth for what's in each Skep release. If
a concept or reference page says *"the current release"* without a
version number, that's because the answer lives here.

## v0.1.0 — current

**LLM backend.** Ships with [Claude Code](https://docs.claude.com/en/docs/claude-code)
as the configured LLM CLI. Classifier defaults to `claude-haiku-4-5-20251001`,
plan-gen and executor default to the model you set with `skep config model`
(typically `sonnet` or `opus`).

**Planned for v0.2.0.** Gemini CLI and Codex CLI presets. The plumbing
is in `internal/llm/presets.go`; the gate is end-to-end testing against
the parallel classify + plan pipeline, the approval watchdog, and
cross-repo delegation.

**Task lifecycle.** Eleven states: `created`, `classified`, `pending`,
`pending_clarification`, `approved`, `queued`, `executing`, `done`,
`failed`, `interrupted`, `rejected`. See [Task lifecycle](/concepts/task-lifecycle/)
for the full state machine.

**MCP tools.** Eleven tools exposed by `skep mcp serve`: `get_overview`,
`search_symbols`, `get_call_graph`, `get_file_context`, `list_tasks`,
`create_task`, `dedup_task`, `create_remote_task`, `get_remote_task`,
`approve_remote_task`, `wait_remote_task`. See [MCP tools reference](/reference/mcp-tools/).

**Indexer.** Native tree-sitter for Go, TypeScript, JavaScript, Python,
Kotlin, Java. Line-based parsers for YAML, Terraform, Dockerfile, JSON.
universal-ctags fallback for Rust, Ruby, Elixir, Lua, C, C++, C#, PHP,
Swift, R, Shell, Bash, SQL, Protobuf, Haskell, Scala, Perl, OCaml, Nim,
Crystal, Make, CMake (22 languages via ctags when the binary is
available; without ctags, those files are indexed by filename only).

**Cockpit.** `skep cockpit setup` writes a tmux config snippet to
`~/.tmux.conf.d/skep.conf`. See [The workspace cockpit](/getting-started/cockpit/).

**Known limitations.**

- Claude Code is the only wired LLM backend. Gemini / Codex fall back
  to Claude if configured.
- Windows is not supported natively. Use WSL2.
- Cross-repo task routing requires the peer daemon to be running.
  Fallback to a direct database write is not yet implemented.

## Earlier

v0.1.0 is the first public release. Internal releases before v0.1.0
are not documented here.
