---
title: Contributing
description: How to build, test, and submit changes to Skep.
---

Skep is a single-maintainer project at v0.2.0. A lightweight process
keeps things moving:

1. **Open an issue first** for anything non-trivial. A quick "here's
   what I'd like to build, does this fit?" saves both of us from a
   rejected PR. Bug fixes and typo corrections can skip straight to
   a PR.
2. **Small, focused PRs.** One concern per change. A PR that also
   "fixes a few unrelated things while I'm in here" is harder to
   review and more likely to stall.
3. **Commit style.** Short imperative subject. Body explains *why*.
   Prefixes like `feat:` / `fix:` / `docs:` / `refactor:` are
   encouraged but not strictly enforced.

The canonical copy of this guide lives at
[`CONTRIBUTING.md`](https://github.com/ChaitanyaPinapaka/skep/blob/main/CONTRIBUTING.md)
in the repo root. This page tracks it.

## Getting the code building

```bash
git clone https://github.com/ChaitanyaPinapaka/skep
cd skep
go build ./...
go test ./...
go install ./cmd/skep
```

- Go 1.21+ for the core binary.
- No CGO. Tree-sitter is via a pure-Go binding; SQLite via
  `modernc.org/sqlite`. Single binary, no shared libraries.
- Run `skep doctor` after install to verify tmux, Claude Code, and
  workspace state.

The docs site lives under `site/` and builds with Node 20:

```bash
cd site
npm ci
npm run build
```

## Project layout

| Path | Purpose |
|---|---|
| `cmd/skep/` | CLI entrypoint and subcommand wiring |
| `internal/agent/` | Executor — spawns Claude Code, handles post-run index diffing |
| `internal/daemon/` | Per-repo agent: task loop, socket server, approval watchdog, file watcher |
| `internal/index/` | SQLite store, tree-sitter symbol extraction, FTS5 triggers |
| `internal/llm/` | Shell-out wrapper, preset registry, prompt templates |
| `internal/mcp/` | Stdio MCP server exposed to Claude Code |
| `internal/tasks/` | Dedup layers, classify+plan pipeline, clarify flow, lifecycle |
| `internal/tmuxutil/` | Shared tmux introspection helpers |
| `internal/trust/` | Out-of-repo `test_cmd` trust store |
| `benchmarks/` | Per-task token benchmarks + dedup precision/recall harness |

## Running tests

```bash
go test ./...
```

There's a golden-file test for prompt rendering at
`internal/llm/prompts/`. If you intentionally change a `.tmpl` file,
regenerate the golden:

```bash
go test ./internal/llm/prompts/... -update
```

Re-run the dedup benchmark after changing a scoring layer:

```bash
cd benchmarks/dedup
go run .
```

The fixture lives at `benchmarks/dedup/pairs.json` and is
hand-labeled. Add new pair rows when you add a new category.

## Scope rules

A few things that will get pushed back during review:

- **New LLM backends.** Claude Code is the only wired backend today.
  Gemini CLI and Codex CLI presets are plumbed in `internal/llm/presets.go`
  but gated on an end-to-end test matrix. Adding them earlier means
  every subsequent change has to be validated against N backends,
  which isn't sustainable for a single maintainer pre-v1.
- **Multi-agent runtime features.** Skep is a task ledger and
  spawner, not a runtime. "The LLM should auto-escalate to a second
  LLM on failure" is firmly out of scope. Claude Code is the agent;
  Skep orchestrates.
- **New CLI verbs.** The `skep task` and `skep workspace` dispatcher
  shapes are deliberate. New verbs need a clear justification beyond
  "it would be nice to have."
- **Cloud, telemetry, accounts.** None of these. Ever.

If you're not sure whether something fits, open the issue first.

## Style notes

- **Error messages** tell the user what to do next, not just what
  went wrong.
- **Wrap errors** with `fmt.Errorf("operation: %w", err)`. No panics
  except at truly unrecoverable points.
- **Comments** explain *why*, not *what*. Well-named identifiers
  handle the what.
- **No `bash -c` shell-outs** outside explicit opt-in paths. The
  `llm.ShlexSplit` tokenizer is the standard way to exec a
  user-configured command safely.
- **Config values are untrusted.** A repo cloned from a teammate
  can contain arbitrary JSON; every field that flows into an
  `exec.Command` or a runtime decision must be validated in
  `internal/config/validateConfig`.

## Documentation

Docs live in `site/src/content/docs/` as Markdown (Starlight).
When you add or rename a public subcommand, config key, or MCP
tool, update the relevant reference page in the same PR:

- `reference/commands.md` for CLI changes
- `reference/config.md` for config keys + env vars
- `reference/mcp-tools.md` for MCP tools
- `concepts/task-lifecycle.md` for state changes
- `advanced/troubleshooting.md` for new failure modes

The docs are a public surface. Be honest about what's shipped:
nothing aspirational, no "we plan to", no "coming soon" on public
pages.

## Security

See [SECURITY.md](https://github.com/ChaitanyaPinapaka/skep/blob/main/SECURITY.md)
in the repo root for the private disclosure process. **Please do
not file public issues for security reports.**

## Signing off

By submitting a PR, you agree that your contribution is licensed
under the same [MIT license](https://github.com/ChaitanyaPinapaka/skep/blob/main/LICENSE)
as the rest of the project.
