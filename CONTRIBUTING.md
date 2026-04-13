# Contributing to Skep

Thanks for looking at the code. Skep is a single-maintainer project
at v0.1.0, so a lightweight process keeps things moving:

1. **Open an issue first** for anything non-trivial. A quick "here's
   what I'd like to build, does this fit?" saves both of us from a
   rejected PR. Bug fixes and typo corrections can skip straight to
   a PR.
2. **Small, focused PRs.** One concern per change. A PR that also
   "fixes a few unrelated things while I'm in here" is harder to
   review and more likely to stall.
3. **Commit style.** Short imperative subject. Body explains *why*.
   Prefixes like `feat:` / `fix:` / `docs:` / `refactor:` are
   encouraged but not strictly enforced — readable English wins over
   pedantic conventions.

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
- `skep doctor` after install verifies tmux, Claude Code, and
  workspace state.

The docs site lives under `site/` and builds with Node 20:

```bash
cd site
npm ci
npm run build
```

## Project layout

- `cmd/skep/` — the CLI entrypoint and subcommand wiring.
- `internal/agent/` — the executor that spawns Claude Code and
  handles post-run index diffing.
- `internal/daemon/` — the per-repo agent: task loop, socket server,
  approval watchdog, file watcher.
- `internal/index/` — SQLite store, tree-sitter symbol extraction,
  FTS5 triggers, incremental re-indexing.
- `internal/llm/` — shell-out wrapper, preset registry, prompt
  template rendering (`prompts/` subdir holds `.tmpl` files).
- `internal/mcp/` — the stdio MCP server exposed to Claude Code.
- `internal/tasks/` — dedup layers (keyword / trigram / tf-idf /
  minhash), classify+plan pipeline, clarify flow, task lifecycle.
- `internal/tmuxutil/` — shared tmux introspection helpers.
- `internal/trust/` — out-of-repo `test_cmd` trust store.
- `benchmarks/` — two harnesses: per-task token cost (`bench.sh`)
  and dedup precision/recall (`dedup/`). Both are maintainer tools,
  not user-facing commands.

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

To re-run the dedup benchmark after changing a scoring layer:

```bash
cd benchmarks/dedup
go run .
```

The fixture lives at `benchmarks/dedup/pairs.json` and is
hand-labeled. Add new pair rows when you add a new category.

## Scope rules for v0.1.0

A few things I will push back on during review:

- **New LLM backends.** v0.1.0 ships Claude Code only. Gemini CLI
  and Codex CLI are planned for v0.2.0 behind an end-to-end test
  matrix. Adding them earlier means every subsequent change has to
  be validated against N backends, which isn't sustainable for a
  single maintainer pre-v1.
- **Multi-agent runtime features.** Skep is a task ledger and
  spawner, not a runtime. "The LLM should auto-escalate to a second
  LLM on failure" or "add a planner agent that supervises the
  executor agent" is firmly out of scope. Claude Code is the agent;
  skep orchestrates.
- **New CLI verbs.** The `skep task` and `skep workspace` dispatcher
  shapes are deliberate. New verbs need a clear justification beyond
  "it would be nice to have."
- **Cloud, telemetry, accounts.** None of these. Ever.

If you're not sure whether something fits, open the issue first.

## Style notes

- **Error messages** should tell the user what to do next, not just
  what went wrong. "Could not read config.json" is not enough;
  "config.json is malformed (json: unexpected token at line 3) —
  fix the file or re-run 'skep init'" is.
- **Wrap errors** with `fmt.Errorf("operation: %w", err)`. No
  panics except at truly unrecoverable points (embedded template
  failing to parse at startup).
- **Comments** explain *why* something is the way it is, not *what*
  it does. Well-named identifiers handle the what.
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
pages (the `/about/#roadmap` page is the only sanctioned place for
forward-looking statements).

## Signing off

By submitting a PR, you agree that your contribution is licensed
under the same MIT license as the rest of the project.
