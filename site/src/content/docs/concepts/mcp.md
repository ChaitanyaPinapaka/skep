---
title: MCP integration
description: Skep exposes a stdio MCP server so your LLM CLI can query the live index mid-coding.
---

Skep exposes a [Model Context Protocol](https://modelcontextprotocol.io/)
stdio server so your LLM CLI can query the live index mid-coding session.
The LLM calls MCP tools to fetch the symbols, call graphs, and task
state it needs; Skep answers from the SQLite index maintained by the
daemon.

## What MCP is, in one sentence

A stdio JSON-RPC protocol that lets an LLM client (currently Claude
Code — see [Changelog](/changelog/)) discover and invoke tools hosted
by a local process.

## One-shot install

```bash
skep mcp install
```

This merges an entry into `~/.claude/settings.json`, preserving any
other MCP servers you already have. Flags:

```bash
skep mcp install --name backend     # install under a custom key
skep mcp install --force            # overwrite an existing entry
```

## Manual install

```json
// ~/.claude/settings.json
{
  "mcpServers": {
    "skep": { "command": "skep", "args": ["mcp"] }
  }
}
```

To pin the server to a specific repo regardless of `cwd`:

```json
{
  "mcpServers": {
    "skep-backend": {
      "command": "skep",
      "args": ["mcp", "--repo", "/home/you/code/my-project/backend"]
    }
  }
}
```

## Tools exposed

See [MCP tools reference](/reference/mcp-tools/) for the full input
schemas and return shapes. At a glance:

| Tool                  | Purpose                                    |
|-----------------------|--------------------------------------------|
| `get_overview`        | architecture, top symbols, active tasks    |
| `search_symbols`      | FTS5 search over the index                 |
| `get_call_graph`      | callers + callees of a symbol              |
| `get_file_context`    | all symbols in a file, signatures only     |
| `list_tasks`          | pending / active / completed tasks         |
| `create_task`         | create a task in this repo                 |
| `dedup_task`          | check whether a proposed task duplicates an existing one, without creating it |
| `create_remote_task`  | create a task in a peer repo (blocks until classify + plan) |
| `get_remote_task`     | read current state of a remote task, including clarification questions when pending |
| `approve_remote_task` | approve a pending task in a peer repo      |
| `wait_remote_task`    | block until a peer task reaches a terminal state |

## Daemon-free

The MCP server reads the SQLite index directly. You don't need the
daemon running to call `get_overview` or `search_symbols`. The daemon
is only required for task execution and cross-repo routing — the
read-only tools work as long as `.skep/index.db` exists.
