---
title: MCP tools reference
description: Every tool exposed by skep mcp — input schemas and return shapes.
---

Every tool exposed by `skep mcp`. Install the server once with
`skep mcp install`, then these are available to any MCP-aware LLM
client. The current release is tested against Claude Code — see [Changelog](/changelog/).

## `get_overview`

High-level view of the current repo: top-ranked symbols, active tasks,
dependency summary. Call this first when starting a coding session.

**Input:** none.

**Returns:** an overview object with fields for architecture summary,
top symbols (by PageRank), active task ids, and peer repos in the
workspace.

## `search_symbols`

FTS5 search over the symbol index.

**Input:**
```json
{
  "query": "string — FTS5 expression, supports wildcards",
  "kinds": ["optional array of symbol kinds: func, method, struct, ..."],
  "limit": "optional integer, default 20"
}
```

**Returns:** an array of matches — each with `id`, `name`, `kind`,
`file_path`, `line`, `signature`, `doc_comment`, and a relevance
`score`.

## `get_call_graph`

Who calls a symbol? What does it call?

**Input:**
```json
{ "symbol_id": "string — id from search_symbols" }
```

**Returns:** `callers` and `callees`, each an array of symbol ids
with file and line information.

## `get_file_context`

Returns every symbol defined in a single file with its signature,
doc comment, and line range. Use this when you need the full shape
of one file without loading its contents.

**Input:**
```json
{ "file_path": "string — repo-relative file path" }
```

**Returns:** an array of symbols with signatures and line ranges.

## `list_tasks`

Pending, active, and completed tasks in the current repo.

**Input:**
```json
{ "status": "optional filter: pending, executing, done, failed, ..." }
```

**Returns:** an array of task objects.

## `create_task`

Create a task in the current repo. Equivalent to `skep task create`,
but callable from the LLM mid-session.

**Input:**
```json
{
  "description": "string — the task",
  "acceptance": "optional string — acceptance criteria"
}
```

**Returns:** `{ task_id, classification, plan }`.

## `dedup_task`

Check whether a proposed task description duplicates an existing task
**without** creating it. Runs the same four cheap dedup layers as
`create_task` (keyword → trigram → tf-idf → minhash) and optionally
the LLM semantic escape hatch. Call this from a classifier before
`create_task` or `create_remote_task` to avoid spawning work that
duplicates something already in flight.

**Input:**
```json
{
  "description": "string — proposed task description",
  "include_llm": "optional boolean — also run the LLM escape hatch (slower, ~5-20s)"
}
```

**Returns:** a `DedupResult`:

```json
{
  "is_duplicate": true,
  "reason": "near-duplicate of task #12 (trigram jaccard=0.87)",
  "task_id": 12,
  "layer": "trigram",
  "score": 0.87
}
```

`layer` is one of `keyword`, `trigram`, `tfidf`, `minhash`, or `llm`.
When nothing matches, `is_duplicate` is `false` and the other fields
are empty. The cheap path typically returns in under 10 ms.

## `create_remote_task`

Create a task in a **peer** repo via its daemon and **block** until
the peer has classified and planned it. The local daemon looks up the
peer socket in the workspace registry, forwards the task, waits for
the peer's parallel classify + plan pipeline to finish, and returns
the merged result.

**Input:**
```json
{
  "repo": "string — peer repo name from the workspace registry",
  "description": "string",
  "classify_timeout_sec": "optional integer — ceiling on the wait, default 120, max 600"
}
```

**Returns:**
```json
{
  "task_id": 17,
  "name": "add-health-endpoint",
  "status": "pending | approved | pending_clarification | ...",
  "classification": "small | large | ambiguous | reject",
  "plan": "rendered plan text",
  "repo": "backend",
  "next_action_hint": "...",
  "clarification_questions": ["optional — only present when status is pending_clarification"]
}
```

When the peer classifier flags the task as ambiguous, the response
includes `clarification_questions` so the calling agent can surface
them to its user instead of waiting silently. `next_action_hint` is
set to a human-readable instruction on what to do next (approve, wait,
answer clarifications, etc).

## `get_remote_task`

Read the current state of a remote task.

**Input:**
```json
{
  "repo": "string — peer repo name",
  "task_id": "integer — id returned by create_remote_task"
}
```

**Returns:** the full task object wrapped in `{ task, clarification_questions?, next_action_hint? }`.
Tasks in `pending_clarification` status include `clarification_questions`
from the peer's `.skep/clarify/<id>.md` file, so the caller can surface
the questions to the user without polling separately.

## `approve_remote_task`

Approve a pending task on a peer repo so its daemon can execute it.
Only call this when you've reviewed the peer's plan — the human gate
is in place for a reason.

**Input:**
```json
{
  "repo": "string — peer repo name",
  "task_id": "integer"
}
```

**Returns:** the updated task state.

## `wait_remote_task`

Block until a remote task reaches a terminal state (`done`, `failed`,
or `interrupted`) or a timeout expires. Use this after
`create_remote_task` when the local plan depends on the peer's result.

**Input:**
```json
{
  "repo": "string — peer repo name",
  "task_id": "integer",
  "timeout_sec": "optional integer, default 1800, cap 3600"
}
```

**Returns:** the final task object, or a timeout error message that
tells the caller to poll with `get_remote_task` later.
