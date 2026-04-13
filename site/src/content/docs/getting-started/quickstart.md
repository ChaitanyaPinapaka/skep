---
title: Quickstart
description: Two minutes. Zero to first completed task.
---

Two minutes. Zero to first completed task.

## 1. Initialize the repo

```bash
cd your-project
skep init
```

`init` is interactive. It asks for the execution model, the test
command, and whether to create a workspace. It then walks your
files, parses them with tree-sitter, writes `.skep/index.db`, and
registers the repo in the workspace. The cockpit tmux config at
`~/.tmux.conf.d/skep.conf` is written automatically. v0.1.0 uses
Claude Code as the LLM backend; additional backends arrive in v0.2.0.

## 2. Start the daemon

```bash
tmux new -s work
skep daemon &
```

Running the daemon in your current tmux session means task panes spawn
right next to you. If you'd rather keep it hidden, see the
[cockpit guide](/getting-started/cockpit/).

## 3. Create a task

```bash
skep task create "add a GET /health endpoint that returns 200 OK"
```

Skep:

1. Runs four cheap local dedup layers (keyword, trigram, tf-idf,
   minhash) against in-flight and pending tasks. Optional LLM
   semantic dedup if none hit.
2. Fires a parallel classify + plan pipeline — Haiku decides
   `small` / `large` / `ambiguous` / `reject` while Opus generates a
   structured plan against the hot index via MCP.
3. Stores the classification, structured plan steps, and plan
   metadata in `.skep/index.db`. Ambiguous tasks park in
   `pending_clarification` with questions in `.skep/clarify/<id>.md`
   — answer them and re-run with `skep task clarify <id>`.

## 4. Review the plan

```bash
skep tasks          # list
skep task show 1    # full plan + classification + reason
```

You'll see something like:

```
Task #1  classified: small
Plan:
  1. Add Handler.Health receiver in internal/http/handlers.go
  2. Register GET /health route in internal/http/router.go
  3. Add handler test in internal/http/handlers_test.go
  4. Run `go test ./...`
```

## 5. Approve and run

```bash
skep task approve 1
skep task run 1
```

`approve` flips the task into the executor queue. `run` spawns a Claude
Code (or your configured CLI) pane that receives the plan as its prompt,
creates a branch `skep/task-1-add-health-endpoint`, edits files, runs
tests, and commits.

When the pane exits, Skep re-indexes, diffs the branch against main,
writes `.skep/task-1-result.md`, and marks the task `done`.

## 6. Review and merge

```bash
git switch skep/task-1-add-health-endpoint
git log --oneline
git diff main
```

Merge or squash the branch yourself. Skep deliberately never touches
`main` — the human is the last reviewer.
