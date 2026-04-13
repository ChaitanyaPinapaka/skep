---
title: Cross-repo delegation
description: Explicit delegation between Skep agents over Unix sockets — contracts cross, diffs don't.
---

Cross-repo work in Skep is **explicit**, not auto-propagated. The
classifier sees peer repos in `<peer_repos>` and emits delegate steps
in the plan. During execution, the LLM calls MCP tools against peer
daemons. Contracts flow between repos; diffs do not.

## The three MCP tools

| Tool                 | What it does                                  |
|----------------------|------------------------------------------------|
| `create_remote_task` | Create a task in another repo (routes via the peer's socket). Returns task id + classification + plan. |
| `approve_remote_task`| Approve a pending task on the peer so its daemon can execute it. |
| `wait_remote_task`   | Block until a remote task reaches `done` / `failed` / `interrupted`, with a timeout. |

## A typical flow

```
sender repo (backend)                    peer repo (frontend)
        │
        │ skep task create "add /export + update JS client"
        ▼
   classify (backend LLM)
   plan:
     1. add handler + route
     2. add tests
     3. delegate to frontend → create_remote_task
     4. wait_remote_task until done
        │
        │ approve
        ▼
   execute in backend pane
        │
        │ step 3: create_remote_task(repo='frontend', ...)
        ├───────────────────── socket ─────────────────────►
        │                                           ▼
        │                                    dedup + classify
        │                                           ▼
        │                                    classified (large)
        │                                           ▼
        │                                    pending (awaits human)
        │
        │ sender pane prints: "peer task #4 pending, awaiting approval"
        │
        │ you switch to the frontend cockpit tab
        │                                    skep task approve 4
        │                                    executing
        │                                    done
        │
        │ step 4: wait_remote_task blocks...
        ◄───────────────────── socket ─────────────────────┤
        │
        │ step 4 returns: done
        ▼
   continue backend plan / finish
```

## Why approvals happen in the peer pane

Large tasks require human approval **on the repo they run in**, not
from the repo that delegated them. This is deliberate: the person
closest to the affected code should see the plan. If you try to
`approve_remote_task` without reviewing the peer's plan, you're
bypassing the human gate.

The sender pane prints the peer task id and a short hint. You switch
tabs in your cockpit (`Ctrl+B <n>`), run `skep task show <id>`, and
approve there. The original sender keeps blocking in
`wait_remote_task` until the peer finishes.

## When the peer daemon isn't running

If the peer daemon isn't up, `create_remote_task` falls back to a
direct write into the peer's `.skep/index.db`. The task is created
in the `created` state; classification will happen the next time the
peer daemon comes up. This is a graceful degradation, not a supported
steady state — keep your daemons running during cross-repo work.
