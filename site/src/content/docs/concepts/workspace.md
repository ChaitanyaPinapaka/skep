---
title: Workspaces
description: How Skep groups related repos so their agents can discover and delegate to each other.
---

A **workspace** groups related repos so their agents can discover and
delegate to each other. You create one during `skep init` when the
CLI asks where the workspace root should live.

## Layout

```
~/code/my-project/                       ← workspace root
├── .skep-workspace/
│   └── registry.json                    ← which repos, which sockets, which LLMs
├── backend/
│   └── .skep/                          ← repo index + config + daemon state
├── frontend/
│   └── .skep/
└── mobile/
    └── .skep/
```

Skep walks up from your current directory to find `.skep-workspace/`,
the same way git walks up to find `.git/`. Any command you run inside a
workspace-rooted directory — even ten levels deep — resolves to the
right registry.

## registry.json

```json
{
  "agents": {
    "backend": {
      "path": "/home/you/code/my-project/backend",
      "socket": "/home/you/code/my-project/backend/.skep/agent.sock",
      "llm": "claude"
    },
    "frontend": {
      "path": "/home/you/code/my-project/frontend",
      "socket": "/home/you/code/my-project/frontend/.skep/agent.sock",
      "llm": "claude"
    }
  }
}
```

The registry is plain JSON, checked and written atomically. Feel free
to open it in an editor if something desynchronizes — deleting a stale
entry is a supported recovery path.

## Cross-repo discovery

When a daemon classifies a task, it passes the list of peer repos to
the LLM as `<peer_repos>`. The classifier can then emit explicit
delegate steps in the plan when a task needs work in a peer — for
example, "Delegate to frontend: update the chart client for the new
`/export` route."

During execution, the LLM invokes the `create_remote_task` MCP tool,
which the local daemon routes over the peer's Unix socket. See
[Cross-repo delegation](/concepts/cross-repo/) for the full flow.

## Listing workspace state

```bash
skep workspace              # same as 'workspace list'
skep tasks --all            # tasks across every registered repo
skep workspace watch        # live dashboard, refreshes every 2s
```

Everything above reads straight from SQLite and the registry — no
daemons required.
