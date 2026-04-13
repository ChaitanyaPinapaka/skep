---
title: "Refactor with MCP tools"
description: "Watch the Skep executor use search_symbols and get_call_graph to rename a type across 40 call sites — without blindly reading any file it doesn't have to."
---

A rename across many files is the canonical refactor that cold-read AI
tools get expensive on. Without an index, the agent has to grep, read
every match, then edit. With Skep's MCP tools the executor asks one
question, gets a precise list of locations, and reads only the files
it actually has to edit.

This walkthrough renames `FooManager` to `FooService` across a Go
codebase with roughly forty call sites spread over twenty files.

## Prerequisites

- A Go repo where `FooManager` is referenced ~40 times.
- `skep init` already run. The classifier already knows where
  `FooManager` lives.

## Step 1: Create the task

```bash
skep task create "rename FooManager to FooService across the codebase, update all call sites and the type definition"
```

```text
task #1 created
classifying...  large
plan ready (6 steps, 20 files affected)
```

## Step 2: What the pipeline sees

Skep runs two LLM calls in parallel on a task create: the cheap
classifier (Haiku, tool-less) decides the scope, while the
plan-generator (Opus) has the Skep MCP server attached. For this
task the plan-generator called `search_symbols("FooManager")`
against the index and saw all 40 references up front — that's how
it could put "20 files affected" into the plan before any code was
touched.

```bash
skep task show 1
```

```text
Task #1
  description : rename FooManager to FooService across the codebase...
  status      : pending
  classified  : large
  reason      : type rename with 40 references in 20 files; mechanical
                but wide blast radius
  files       : 20 (see plan)

Plan:
  1. Rename type FooManager → FooService in internal/foo/manager.go
  2. Rename constructor NewFooManager → NewFooService
  3. Update method receivers (m *FooManager) → (m *FooService)
  4. Update all 38 call sites in 19 dependent files (see search_symbols
     result for the full list)
  5. Update tests in internal/foo/manager_test.go
  6. Run `go test ./...`
```

## Step 3: Approve and run

```bash
skep task approve 1
skep task run 1
```

A Claude pane opens with the plan as its prompt and the Skep MCP server
already wired in.

## Step 4: What Claude does differently with MCP

If you tail the Claude session JSONL while the task runs, you can see
the tool call trace. The interesting part is what Claude does *first*:

```text
↓ tool: search_symbols
  args: {"query": "FooManager", "limit": 50}
↑ result: 40 matches across 20 files
  internal/foo/manager.go:14    type FooManager struct
  internal/foo/manager.go:42    func NewFooManager(...) *FooManager
  internal/foo/manager.go:67    func (m *FooManager) Start(...)
  internal/foo/manager.go:91    func (m *FooManager) Stop(...)
  internal/api/server.go:23     manager *FooManager
  internal/api/server.go:58     m := foo.NewFooManager(cfg)
  ...
```

```text
↓ tool: get_call_graph
  args: {"symbol_id": "internal/foo/manager.go:FooManager"}
↑ result: 14 callers, 6 callees
```

Together those two MCP calls cost roughly 800 input tokens and return
roughly 1.8k tokens — *and Claude now knows every file it needs to
touch, with line numbers*.

Only after that does Claude start reading files — and it reads exactly
the seven files where the structural change is non-trivial (the type
definition, the method receivers, and a handful of consumers with
non-obvious construction patterns). The remaining thirteen files are
mechanical edits that Claude can perform from the search results
alone, without a `Read`.

```text
↓ tool: Read   internal/foo/manager.go
↓ tool: Read   internal/foo/manager_test.go
↓ tool: Read   internal/api/server.go
↓ tool: Read   internal/api/server_test.go
↓ tool: Read   internal/worker/pool.go
↓ tool: Read   internal/worker/pool_test.go
↓ tool: Read   cmd/foo/main.go
↓ tool: Edit   internal/foo/manager.go         (×3 edits — type, ctor, methods)
↓ tool: Edit   internal/foo/manager_test.go    (×4 edits)
... (32 more targeted Edits)
↓ tool: Bash   git commit -am "refactor: rename FooManager to FooService"
```

## Step 5: Review the diff

```bash
git diff main..skep/task-1-rename-foomanager-to-fooservice --stat
```

```text
 internal/foo/manager.go            | 24 ++++++++++++------------
 internal/foo/manager_test.go       | 16 ++++++++--------
 internal/api/server.go             |  8 ++++----
 ...
 20 files changed, 84 insertions(+), 84 deletions(-)
```

Mechanical, symmetric, no surprise edits in unrelated files.

## Token breakdown

Numbers below are illustrative — your real run will vary by repo size,
model, and prompt cache state. For reproducible measurements see
[Benchmarking Skep](/advanced/benchmarking/).

| Tool call                  | Cost                          |
|----------------------------|-------------------------------|
| `search_symbols` (×1)      | ~500 tokens in, 1.2k out      |
| `get_call_graph` (×1)      | ~300 tokens in, 600 out       |
| `Read` (×7 targeted files) | ~14k tokens                   |
| `Edit` (×38 surgical)      | ~2k tokens                    |
| **Effective input**        | **~18k tokens**               |

A typed symbol search returns exactly the symbols that match, and the
call graph returns exactly the call sites that reference them. The
executor then reads only the files that contain real call sites and
emits one `Edit` per site.

## What just happened

Skep's MCP tools expose the structural index directly to the LLM.
When the executor asks `search_symbols("FooManager")`, skep reads
from the SQLite FTS5 index built by tree-sitter at init time — no
file walk, no full-text scan of the repo. `get_call_graph` then
returns the edges stored alongside the symbols during indexing.
The refactor runs against the results.

For larger refactors the gap widens. A rename across 200 files has the
same shape; the cold-read cost grows linearly, the indexed cost grows
roughly with the number of *interesting* files.
