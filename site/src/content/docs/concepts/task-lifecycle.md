---
title: Task lifecycle
description: The state machine every Skep task moves through, and what drives each transition.
---

Every task is a row in the `tasks` table of `.skep/index.db`. It moves
through a small state machine. Each transition is driven by a specific
command, daemon behavior, or LLM response.

A few terms used throughout this page:

- **Classifier** — the LLM shell-out that decides a task's scope
  (`small` / `large` / `ambiguous` / `reject`), its confidence, and
  whether it needs clarification. Defaults to Haiku on Claude because
  it only has to make a decision, not write code.
- **Plan-gen** — the LLM shell-out that produces the structured
  step-by-step plan. Runs in parallel with the classifier and uses
  the execution model (Opus / Sonnet), because plan quality is what
  you pay the expensive model for.
- **Dedup** — four cheap local layers (keyword BM25, character
  trigrams, TF-IDF, MinHash LSH) run before either LLM call to detect
  "this task already exists or is already in flight."
- **Approval watchdog** — the daemon's polling loop that watches each
  executing task's tmux pane for "Do you want to proceed?"-style
  prompts and surfaces them in the status line.

## State diagram

```
                       create_task
                            │
                            ▼
                       ┌─────────┐
                       │ created │
                       └─────────┘
                            │
                            │  dedup: keyword → trigram → tf-idf → minhash
                            │         (LLM semantic escape hatch if all miss)
                            ▼
              ┌─────────────────────────────────┐
              │        classify (Haiku)         │
              │   ┌───────────────────────┐     │
              │   │ runs in parallel with │     │
              │   │  plan-gen (Opus+MCP)  │     │
              │   └───────────────────────┘     │
              └────────────────┬────────────────┘
                               │
         ┌─────────────────────┼─────────────────────┬──────────────┐
         │ small + auto-exec   │ large / ambiguous   │ needs clarify│ reject
         ▼                     ▼                     ▼              ▼
    ┌──────────┐          ┌─────────┐          ┌──────────────┐  ┌──────────┐
    │ approved │          │ pending │          │   pending_   │  │ rejected │
    └──────────┘          └─────────┘          │ clarification│  └──────────┘
         │                     │               └──────┬───────┘
         │        task approve │                      │ task clarify <id>
         │◄────────────────────┘                      │  → re-runs pipeline
         │                                            │
         │◄───────────────────────────────────────────┘
         ▼
    ┌─────────┐
    │ queued  │    (executor dequeues serially)
    └─────────┘
         │
         ▼
    ┌───────────┐   step-level execution: one shell-out per plan step,
    │ executing │   per-step retry (one), first-failure-stops-task,
    └───────────┘   approval watchdog polls every 2s
         │
         ├──► done         (commits merged into base branch)
         ├──► failed       (executor error, retryable via task run)
         └──► interrupted  (Ctrl+C or pane closed — session resumable)
```

## What drives each transition

| Transition | Driver |
|---|---|
| `→ created` | `skep task create`, MCP `create_task`, peer `create_remote_task` |
| `created → dedup → pipeline` | all four cheap layers (keyword, trigram, tf-idf, minhash) pass, optionally the LLM escape hatch passes too |
| `pipeline → pending` | large or ambiguous classification (waits on human approval) |
| `pipeline → approved` | `small` classification **and** `auto-execute-small=true` |
| `pipeline → pending_clarification` | classifier returned `needs_clarification=true` — questions saved to `.skep/clarify/<id>.md` |
| `pipeline → rejected` | classifier returned `classification=reject` (destructive, not actionable, etc.) |
| `pending_clarification → pending/approved` | `skep task clarify <id>` — merges answers into the description and re-runs the pipeline |
| `pending → approved` | `skep task approve <id>` |
| `approved → queued` | executor picks it up |
| `queued → executing` | executor spawns a branch + task pane |
| `executing → done` | pane exits, commits merged into base |
| `executing → interrupted` | Ctrl+C or pane closed — resume with `skep task run <id>` |
| `executing → failed` | executor error, retry via `skep task run <id>` |
| `* → done` (force) | `skep task done <id>` |
| `* → deleted` | `skep task delete <id>` |

## Step-level execution

When a task is approved, its plan is materialized into `task_steps`
rows — one per `PlanStep` from the plan-generation pipeline — keyed
on `(task_id, seq)`. The executor then runs one shell-out per step
rather than a single shell-out for the whole plan.

Per step:

1. Mark the row `executing`.
2. Shell out with a focused per-step prompt (verb, target file,
   symbols, acceptance, step description). If `step_model_by_verb`
   routes this verb to a specific model, the command template is
   rewritten to inject `--model <value>`.
3. Capture `HEAD` before and after. A commit that lands during the
   shell-out is recorded in `task_steps.commit_sha`.
4. On success: mark `done`, move to the next step.
5. On failure: retry **once**, carrying the previous stdout/stderr +
   error into the retry prompt so the model can correct course.
6. On second failure: mark the step `failed`, mark the task `failed`,
   stop. This is "first-failure-stops-task" — later steps are not
   attempted, so no partial damage.

`skep task show <id>` renders each step with a glyph
(`✓` done, `✗` failed, `→` executing, `·` skipped), the verb,
description, target file, commit SHA, duration, and retry count. A
failed step's output is indented under its row so the blocker is
visible without opening the result file.

Resuming an interrupted task is still `skep task run <id>` — the
step loop picks up from the first non-terminal row, so completed
steps are not re-run.

## The approval watchdog

While a task is `executing`, the daemon polls its tmux pane every two
seconds and matches the tail against a list of approval-prompt regexes
("Do you want to proceed?", "Continue? [y/n]", ...). On match:

1. The task row gets `needs_input=1` in the database.
2. The tmux window name is prefixed with `[!]`.
3. `skep status --oneline` surfaces `🔔 #<id> waiting approval`, which
   the cockpit tmux status bar renders automatically.
4. `Ctrl+b !` (bound to `skep task jump-pending` by the cockpit config)
   jumps the client to the waiting window so you can answer in place.

The flag clears the moment the prompt disappears from the pane — nothing
to reset manually. Disable the watchdog by setting
`"approval_watchdog": false` in `.skep/config.json`.

## Post-execution

When a task finishes (any terminal state), the daemon:

1. Re-indexes the repo so the next classification sees new symbols.
2. Diffs the branch against base — no commits means `interrupted`.
3. Writes a markdown summary to `.skep/task-<id>-result.md`.
4. Emits a dashboard update so `skep workspace watch` reflects it live.

Resuming an interrupted task is just `skep task run <id>` — the
executor replays the plan, and your LLM CLI picks up its previous
session via its `--resume` flag when supported.
