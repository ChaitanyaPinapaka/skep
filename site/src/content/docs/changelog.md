---
title: Changelog
description: Release notes for Skep. Version-specific details (supported LLM backends, state machine changes, breaking config) live here.
---

This page is the source of truth for what's in each Skep release. If
a concept or reference page says *"the current release"* without a
version number, that's because the answer lives here.

## v0.2.0 — current

**HTTP API sidecar.** Each daemon now listens on a random loopback
port (`127.0.0.1:<port>`) alongside its Unix socket. The URL and a
randomly-generated bearer token are written to `.skep/http.json`
(mode 0600). Clients that cannot speak the Unix-socket protocol —
VS Code extensions, webhook receivers, and future remote surfaces —
hit the HTTP endpoints with the same JSON verbs the socket accepts.
Everything stays on `127.0.0.1`; there is no network exposure.

**Step-level task execution.** When a task is approved, its
plan is materialized into a new `task_steps` table keyed on
`(task_id, seq)`. The executor then shells out **per step** instead
of once per task, with a per-step retry, per-step commit-SHA
capture, and a first-failure-stops-task policy. `skep task show <id>`
renders each step with status glyphs (`✓`/`✗`/`→`/`·`), verb,
target file, commit SHA, duration, and retry count. See
[Task lifecycle](/concepts/task-lifecycle/) for the full flow.

**Per-verb model routing (infrastructure).** New
`step_model_by_verb` config key — a map from plan verb to model name
— rewrites the per-step command template to inject `--model <value>`
at dispatch. Empty default routes everything to `model`; populate
the map once you have data on which verbs need which model. See
[Config reference](/reference/config/#per-verb-step-routing).

**MCP parity for local task verbs.** Five new tools in `skep mcp`:
`show_task`, `approve_task`, `reject_task`, `clarify_task`, and
`delete_task`. Classifying LLMs can now drive the whole local task
lifecycle over MCP instead of dropping to the CLI mid-session. See
[MCP tools reference](/reference/mcp-tools/).

**CLI polish.** `--json` is now supported on every write verb
(`task create / approve / reject / clarify / delete`), not just
reads. `skep task create -` reads the task description from stdin.
`skep completion <bash|zsh|fish>` emits a shell completion script
you can source or drop into the appropriate completions directory.

**Integration test harness.** Internal: a multi-repo fixture runner
lives in `internal/testkit` and drives the CLI end-to-end against
temporary workspaces. Not user-facing; unblocks future releases.

**Known limitations (still).**

- Claude Code is the only wired LLM backend. Gemini / Codex presets
  remain gated on end-to-end testing — the preset plumbing is in
  `internal/llm/presets.go` and ready to flip on.
- Windows is not supported natively. Use WSL2.

## v0.1.0

**LLM backend.** Ships with [Claude Code](https://docs.claude.com/en/docs/claude-code)
as the configured LLM CLI. Classifier defaults to `claude-haiku-4-5-20251001`,
plan-gen and executor default to the model you set with `skep config model`
(typically `sonnet` or `opus`).

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

## Earlier

v0.1.0 is the first public release. Internal releases before v0.1.0
are not documented here.
