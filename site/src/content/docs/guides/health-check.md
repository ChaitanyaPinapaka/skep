---
title: "Add a health endpoint"
description: "A beginner walkthrough: classify a task, review its plan, approve it, and watch Skep execute end-to-end on a git branch."
---

The smallest meaningful task you can run through Skep: add a `GET /health`
endpoint to a Go web service. Five minutes start to finish. By the end
you will have walked the full task lifecycle — create, classify, plan,
approve, run, review, merge — without any of the parts being abstract.

## Prerequisites

- Skep installed and `skep doctor` reports green.
- A Go repo with a `go.mod` and at least one HTTP handler file.
- The repo has been through `skep init` (you have a `.skep/` directory).

## Step 1: Create the task

```bash
skep task create "add a GET /health endpoint returning 200 OK"
```

```text
task #1 created
classifying...  small
plan ready (4 steps, 1 file affected)
```

Skep runs four cheap local dedup layers, then fires classify (Haiku)
and plan-generation (Opus against the MCP index) in parallel. The
merged result — classification, structured plan steps, and metadata
— lands in `.skep/index.db` within a few seconds.

## Step 2: Review the plan

```bash
skep task show 1
```

```text
Task #1
  description : add a GET /health endpoint returning 200 OK
  status      : pending
  classified  : small
  reason      : single handler + single route registration, no schema change
  branch      : (not yet created)
  files       : internal/http/router.go

Plan:
  1. Add Handler.Health receiver in internal/http/handlers.go
  2. Register GET /health in internal/http/router.go
  3. Add a handler test in internal/http/handlers_test.go
  4. Run `go test ./...`
```

`small` means Skep is confident this is a contained change. The plan is
explicit — no "implement the feature" hand-waving — so when the executor
runs you can predict what will happen.

## Step 3: Approve

```bash
skep task approve 1
```

```text
task #1 approved
```

Approval flips the task into the executor queue. Nothing else moves
until you ask it to.

## Step 4: Run it

```bash
skep task run 1
```

If your daemon is running inside a tmux cockpit, this opens a new pane
with Claude Code already loaded with the plan as its prompt. Without
the daemon, Claude opens directly in your current terminal.

You can watch Claude work in real time. It will:

1. Create a branch `skep/task-1-add-a-get-health-endpoint`.
2. Edit `handlers.go`, `router.go`, and `handlers_test.go`.
3. Run `go test ./...`.
4. Commit the changes.
5. Exit cleanly.

When the pane closes, Skep re-indexes the repo, diffs the new branch
against `main`, and writes `.skep/task-1-result.md`.

## Step 5: Review the result

```bash
skep task show 1
```

```text
Task #1
  status      : done
  branch      : skep/task-1-add-a-get-health-endpoint
  tokens used : 14,820
  result      : .skep/task-1-result.md
```

(Output shape is illustrative — real `skep task show` output may
include additional fields and the exact token count varies per run.)

```bash
git log skep/task-1-add-a-get-health-endpoint --oneline
```

```text
3f1a8c2  test(http): add Handler.Health test
b9d2e41  feat(http): register GET /health route
4e08aa9  feat(http): add Handler.Health returning 200 OK
```

Three small commits, in the order the plan called for. The result file
has the full diff and the test output.

## Step 6: Merge

```bash
git checkout main
git merge skep/task-1-add-a-get-health-endpoint
```

Skep deliberately never touches `main`. The branch is yours to merge,
squash, rebase, or throw away. The human is always the last reviewer.

## What just happened

- **MCP tools used by the executor:** `search_symbols` (to find your
  router), `get_file_context` (to see existing handlers), then targeted
  `Read` + `Edit` calls. No blind file reads.
- **Token cost:** ~15k effective input tokens for a four-step task — an
  illustrative figure; real runs vary with repo size and model.
- **Lifecycle states traversed:** `created → pending → approved →
  queued → executing → done`. With `auto_execute_small` enabled,
  the `pending → approved` step auto-fires and you land in
  `queued → executing` without an approval step. See
  [Task lifecycle](/concepts/task-lifecycle/) for the full state
  machine including the `pending_clarification` branch.

The first sliver of honey in your hive. Try
[Cross-repo auth rewrite](/guides/cross-repo-auth/) next to see what
happens when a task spans two repos.
