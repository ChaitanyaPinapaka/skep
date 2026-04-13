---
title: Configuration reference
description: Every key in .skep/config.json — defaults, valid values, and how to set them.
---

Skep configuration lives in `.skep/config.json` per repo. Set values
during `skep init` or change them any time with `skep config <key>
<value>`. Dump everything with `skep config list`.

## Keys

### LLM backends

| Key | Default | Description |
|-----|---------|-------------|
| `llm` | `claude` | LLM CLI preset for task execution. The current release accepts `claude` only; additional presets are gated on end-to-end testing — see [Changelog](/changelog/). |
| `llm_classify` | same as `llm` | LLM CLI preset used for the classify + plan pipeline. |
| `llm_dedup` | same as `llm_classify` | LLM CLI preset used for the LLM semantic dedup escape hatch. Runs after the four cheap layers when they all miss. |
| `model` | — (preset default) | Execution model. E.g., `sonnet`, `opus`. |
| `model_classify` | `claude-haiku-4-5-20251001` | Classifier model. Haiku is the default because the classifier only decides size + confidence + clarify/reject — Opus-level reasoning is wasted here. |
| `model_dedup` | `claude-haiku-4-5-20251001` | Dedup model. Same reasoning as classifier — "are these two sentences about the same thing" is a Haiku-class question. |

The plan-generation half of the pipeline always uses the execution model
(`model`) because plan quality is what you pay the expensive model for.

### Task execution

| Key | Default | Description |
|-----|---------|-------------|
| `test-cmd` | auto-detected | Shell command run after task execution. |
| `tmux-layout` | `split-h` | How task panes open. See [Pane layout](#pane-layout) below. |
| `auto-execute-small` | `false` | When `true`, the daemon auto-approves and runs any task classified `small`. Large / ambiguous / `pending_clarification` tasks still gate on a human. |
| `workspace` | — | Absolute path to the workspace root. Set by `skep init`. |

### Approval watchdog

| Key | Default | Description |
|-----|---------|-------------|
| `approval_watchdog` | `true` | Whether the daemon polls each executing task's tmux pane for confirmation prompts. Set to `false` to disable the `[!]` window prefix and `skep status --oneline` bell. |
| `approval_patterns` | built-in defaults | JSON array of Go regexps matched against the tail of each task pane. Empty or missing means use the built-ins (covers Claude Code's standard phrasings). |

Example:

```json
{
  "approval_watchdog": true,
  "approval_patterns": [
    "Do you want to proceed\\?",
    "Continue\\? \\[y/n\\]",
    "Apply these edits\\?"
  ]
}
```


## Pane layout

```bash
skep config tmux-layout split-h       # vertical split, new pane on the right (default)
skep config tmux-layout split-v       # horizontal split, new pane on the bottom
skep config tmux-layout window        # new tmux window per task
skep config tmux-layout popup         # floating popup (tmux ≥ 3.2)
```

## Example: Claude with Opus for execution, Haiku for classify

```bash
skep config llm claude
skep config model opus                # Opus for the plan-gen and executor
skep config test-cmd 'go test ./...'
skep config tmux-layout window
skep config auto-execute-small true
```

`model_classify` and `model_dedup` already default to Haiku 4.5 when
the preset is `claude`, so you do not need to set them by hand.

## Where values come from

Precedence, highest to lowest:

1. Environment variable — only for the specific runtime knobs listed in [Environment variables](#environment-variables) below. These do not override `llm`, `model`, or other config.json values.
2. `.skep/config.json` in the current repo.
3. Built-in default.

## Environment variables

Env vars override the corresponding config.json values and are the
quickest way to try a different threshold without touching config.

| Variable | Default | Effect |
|---|---|---|
| `SKEP_DIR` | `<repo>/.skep` | Override the `.skep` directory location for this command. Used by `skep mcp --repo` and by test harnesses that need to point at a sandbox index. |
| `SKEP_CLASSIFY_TIMEOUT` | `180` (seconds) | Ceiling on the parallel classify + plan pipeline. A stalled LLM CLI cannot hang your shell longer than this. |
| `SKEP_CLASSIFY_MCP` | off | Set to `1`/`true`/`yes`/`on` to attach the Skep MCP server to the classifier's shell-out, so the classifier can call `search_symbols` / `get_file_context` mid-decision. Off by default — the classifier is a decision-only step, and MCP round trips roughly double its cost. |
| `SKEP_PLAN_MCP` | on | Set to `0`/`false` to disable MCP for the plan-generation half (offline demos, CI runs, or debugging the static-context prompt). Classifier stays tool-less either way. |
| `SKEP_DEDUP_TIMEOUT` | `60` (seconds) | Ceiling on the LLM semantic-dedup escape hatch. |
| `SKEP_DEDUP_BM25_THRESHOLD` | `0.80` | Word-overlap ratio above which the keyword layer flags a duplicate. Lower = more recall, more false positives. |
| `SKEP_DEDUP_TRIGRAM_THRESHOLD` | `0.55` | Character 3-gram Jaccard threshold for the second layer. |
| `SKEP_DEDUP_TFIDF_THRESHOLD` | `0.60` | TF-IDF cosine threshold for the third layer. |
| `SKEP_DEDUP_MINHASH_THRESHOLD` | `0.70` | MinHash LSH similarity threshold for the fourth layer. |

Tune dedup thresholds by running the included benchmark:

```bash
cd benchmarks/dedup && go run .
```

See [Benchmarking Skep](/advanced/benchmarking/) for the full story.

## Test command trust

Running `test_cmd` after a task execution is gated on an out-of-repo
trust store, not on a `trusted` flag in `.skep/config.json`. This
prevents a committed `config.json` in a cloned repo from silently
enabling command execution on a collaborator's machine.

The store lives at `~/.skep/trusted-repos.json` and records, per
repo absolute path, the sha256 of the `test_cmd` the user approved
during `skep init`:

```json
{
  "entries": [
    {
      "path": "/home/you/code/backend",
      "test_cmd_hash": "a3b5...",
      "trusted_at": "2026-04-12T18:22:30Z"
    }
  ]
}
```

If `test_cmd` changes after the initial approval (by edit, pull, or
merge), the hash stops matching and the executor silently skips the
test run on the next task. The user has to re-run `skep init` to
acknowledge the new value. `skep doctor` shows
`test_cmd_trusted=true|false` for the current repo so you can check
the state at a glance.

## Editing config.json by hand

`.skep/config.json` is plain JSON. Edit it directly if you prefer —
values are validated the next time the daemon or CLI loads the file.
A malformed `config.json` produces a single-line warning on stderr
and falls back to defaults rather than blocking the command.
