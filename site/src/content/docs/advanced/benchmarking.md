---
title: Benchmarking Skep
description: The three-tier observability policy and why benchmarking is a maintainer tool, not a CLI verb.
---

:::tip[For day-to-day cost visibility]
Every task row has a `tokens_used` field, visible in
[`skep task show`](/reference/commands/#tasks) and `skep workspace watch`.
That's the always-on layer — no flag, no opt-in. The benchmarking
machinery described here is for producing reproducible A/B numbers,
not for everyday cost tracking.
:::

> **This is a maintainer / evaluator tool, not a CLI verb.**
>
> Skep deliberately does NOT ship a `skep benchmark` top-level command.
> Benchmarks run through a script from a checked-out copy of the skep
> repo; they are not part of the daily user surface. The tool's always-on
> observability (`tokens_used` on every task, visible in `skep task show`
> and `skep workspace watch`) covers the "what did this cost me?"
> question for everyday users. The harness in `benchmarks/` is for
> producing reproducible A/B numbers — the kind you put in a blog post
> or README table.

The harness measures the real token cost of running a task through
Skep vs running the same task with plain `claude -p` and no Skep MCP.

## The three-tier observability policy

| Layer | What | Where it lives | Who runs it |
|---|---|---|---|
| **1. Always-on** | `tokens_used` + `tools_used_json` on every task row | Built into the executor (`internal/agent/session_usage.go`) | Runs automatically on every `skep task run`. No flag, no opt-in. |
| **2. A/B harness** | `bench.sh` + `aggregate.go` | `benchmarks/` in the skep repo | Maintainers and evaluators, manually, once per numbers-producing session |
| **3. No CLI verb** | — | — | — |

**There is no `skep benchmark` command, no `--benchmark` flag on
`skep task create`, and no `--benchmark` flag on `create_remote_task`.**
That is intentional and explained at the bottom of this page.

## What is measured

Three numbers per `(repo × task)`:

1. **Classifier+plan** — one-shot LLM call with a pre-built context from
   the index. Skep-only; no baseline comparison exists because without
   skep there is no classifier at all. Captured as wall clock + exit code.

2. **Executor baseline** — plain `claude -p "<task>" --allowedTools
   Edit,Write,Bash`, no MCP, no pre-built plan. Claude explores the repo
   with `Read`/`Grep`/`Glob` as needed. This is what you'd pay to run the
   task without skep.

3. **Executor with MCP** — same `claude -p "<task>"` but with
   `--mcp-config` pointing at skep's stdio MCP server. Claude can call
   `search_symbols`, `get_file_context`, `get_call_graph`, etc. instead
   of reading files cold.

**The comparison that matters:** `(classifier + executor_with_MCP)` vs
`executor_baseline`. Skep wins when the sum is smaller.

## Running

```bash
# 1. Build the measurement tool
(cd benchmarks/measure && go build -o ../measure.bin .)

# 2. Run the matrix. Pass one or more already-initialized repo paths.
./benchmarks/bench.sh ~/code/backend ~/code/mobile

# 3. Aggregate the results into a markdown table.
go run ./benchmarks/aggregate.go benchmarks/results/<timestamp>/ \
  > benchmarks/results/<timestamp>/results.md
```

Each task runs three times per repo (classify → baseline → with-MCP),
each on a throwaway branch that is deleted after measurement. Your
working tree is not touched.

## Prerequisites

- `skep` installed on PATH (`make install`)
- `claude` (Claude Code CLI) on PATH
- Each target repo already through `skep init` with a valid llm
  configured (the classify step uses the configured classify model)
- Go 1.21+ for the measure tool and aggregator

## Dedup benchmark

A second, smaller harness lives at `benchmarks/dedup/`. It evaluates
the four cheap dedup layers (keyword → trigram → tf-idf → minhash)
against a labeled fixture of task-description pairs and reports
per-layer precision / recall / F1 plus per-category recall.

```bash
cd benchmarks/dedup
go run .
```

Use this to tune the `SKEP_DEDUP_*_THRESHOLD` env vars for your repo's
real task history. The shipped fixture has 38 hand-labeled pairs across
ten categories (exact, morphological, reorder, paraphrase, synonym,
near-miss, direction-reversed, shared-rare-word, same-surface-different-fix,
report-vs-task). See the `benchmarks/dedup/README.md` in the repo for
full usage and fixture format.

## Why per-turn prompt cache matters

Claude Code heavily uses Anthropic's prompt cache — the first turn pays
full price, subsequent turns read ~90% of the context from cache at 10%
of the cost. The measure tool emits both the raw
`cache_read_input_tokens` and an `effective_input_billed` which
approximates the real billed amount as:

```
effective_input_billed = input_tokens + 0.1 × cache_read_input_tokens
```

The aggregated markdown table reports `effective_input_billed` because
that's what your Anthropic invoice actually reflects. Raw input_tokens
overstate cost once you've been in a session for a few turns.

## Fixing the task set

Tasks live in `benchmarks/tasks/tasks.yaml`. The defaults aim for the
five common archetypes (bug / feature / refactor / test / docs) and are
sized to complete in under 5 minutes on a medium repo. Add or replace
them if you want to benchmark a specific workload.

## Caveats

- The harness measures classifier wall clock but not classifier token
  cost directly. Classify shells out to `claude -p` and does not emit
  a session JSONL the way an interactive Claude Code run does, so the
  per-classify token count is not recoverable from the harness.
- The "baseline" variant runs plain `claude -p` with no MCP. If you
  want to compare against a different tool you'll need to adapt the
  harness.
- Wall-clock numbers reflect LLM latency, not harness overhead.

## Why this isn't a CLI verb

A `skep benchmark` subcommand would make benchmarking feel like an
everyday operation. It isn't. Producing a reproducible A/B number:

- Doubles the LLM cost of the task being measured (once with MCP, once
  without)
- Doubles the wall clock
- Only makes sense on a curated set of canonical tasks (the ones in
  `benchmarks/tasks/tasks.yaml`), not on whatever real work the user
  happens to have in their queue
- Produces artifacts (per-variant JSON rows, aggregated markdown table)
  that are intended for a blog post, not for the user's daily flow

Putting this in the main CLI help menu implies *"run this sometimes"*
and invites the question *"when should I benchmark?"* — a question with
no good answer. The right answer is **"never, unless you're writing a
blog post about skep."**

Look at the neighbors for the established pattern:

| Tool | Has `<tool> benchmark`? | Where benchmarks live |
|---|---|---|
| git | no | outside |
| kubectl | no | kube-burner, k6-kubernetes |
| helm | no | outside |
| docker | no | outside |
| aider (the closest peer) | no | separate harness, maintainers only |

Only `cargo bench` is a counterexample, and it measures *user code*
performance — not cargo's own.

## Reproducing a published Skep benchmark result

If you see a token-savings number in the Skep README, blog post, or
docs site, you can reproduce it yourself by cloning the skep repo and
running `bench.sh` against a few of your own initialized repos. The
script is the source of truth; the numbers in marketing materials are
derived from it.
