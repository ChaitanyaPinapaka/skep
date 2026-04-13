---
title: "Cross-repo auth rewrite"
description: "A full walkthrough of a task that spans backend and mobile: the sender blocks on the peer, the peer classifies and executes, the sender resumes with the real output."
---

The flagship cross-repo walkthrough. Two repos in one workspace —
`backend` (Go) and `mobile` (Kotlin). The mobile client currently
authenticates directly against a third-party session provider;
product wants the mobile app to talk to new `/auth/*` routes on the
backend instead. The backend has to expose those routes *first*,
then the mobile app can switch its client over.

This is the kind of change cross-repo agents were built for. One
human, two repos, two LLM sessions, and Skep keeps them in step.

## Prerequisites

- Both repos in the same workspace (each has been through `skep init`).
- Both daemons running in their own hidden tmux sessions
  (`skep-backend`, `skep-mobile`).
- A workspace cockpit open: `tmux new -s work`, then
  `tmux split-window -h 'skep workspace watch'` in one pane.

If you've never set up a cockpit, see [Cockpit setup](/getting-started/cockpit/).

## Step 1: Create the task in mobile

The mobile repo is the *sender* — it has the requirement, and it
delegates the contract work to the backend. Run this from the mobile
repo's working directory:

```bash
cd ~/code/mobile
skep task create "switch authentication from direct session-provider calls to the new backend /auth/login, /auth/refresh, /auth/logout routes. Delete LegacyAuthApi, add AuthApi, update NetworkModule and the LoginViewModel"
```

```text
task #1 created
classifying...  large
plan ready (8 steps, peer delegation: backend)
```

The classifier saw `backend` listed in `<peer_repos>` and emitted an
explicit delegate step. `large` means Skep is going to wait for you
before it touches anything.

## Step 2: Review the plan

```bash
skep task show 1
```

```text
Task #1
  description : switch authentication from direct session-provider calls
                to the new backend /auth/login, /auth/refresh, /auth/logout
                routes...
  status      : pending
  classified  : large
  reason      : multi-file refactor + cross-repo contract dependency
  peer        : backend
  files       : app/src/main/java/com/example/net/LegacyAuthApi.kt,
                app/src/main/java/com/example/net/NetworkModule.kt,
                app/src/main/java/com/example/auth/LoginViewModel.kt

Plan:
  1. create_remote_task(repo="backend",
       description="add POST /auth/login, /auth/refresh, /auth/logout
                    routes. Return JWT on login.")
  2. approve_remote_task(repo="backend", task_id=<from step 1>)
  3. wait_remote_task(repo="backend", task_id=<from step 1>,
                      timeout_sec=1800)
  4. Add AuthApi.kt with login/refresh/logout calls against /auth/*
  5. Update NetworkModule.kt to inject AuthApi instead of LegacyAuthApi
  6. Update LoginViewModel.kt to call AuthApi.login
  7. Delete LegacyAuthApi.kt
  8. Run ./gradlew test
```

The shape is what matters. Steps 1–3 are the cross-repo dance. Steps
4–8 are the local mobile changes that depend on the backend being
done first. Skep has linearized the dependency for you.

## Step 3: Approve the mobile task

```bash
skep task approve 1
```

```text
task #1 approved
```

## Step 4: Run the mobile task

```bash
skep task run 1
```

Now watch the cockpit. In order:

**1. Mobile Claude pane opens.** The plan is already in its prompt.
Claude reads step 1 and calls `create_remote_task`.

**2. Backend daemon classifies.** The cockpit dashboard pane shows a
new task appear under `backend`:

```text
#1 mobile   executing  [large]  step 1/8
#2 backend  pending    [large]  add POST /auth/login...
```

`create_remote_task` blocks the mobile session until the backend has
classified — a few seconds — then returns the backend's plan to the
mobile Claude as the tool result.

**3. Backend pane appears.** The backend's daemon spawns its own
Claude pane in your cockpit. It's waiting for approval; humans gate
large tasks on the repo they run in. You don't need to switch
anywhere — the mobile session is going to ask the backend to approve.

**4. The mobile session calls `approve_remote_task`.** Step 2 of the
plan. The backend task flips from `pending` to `approved`, then the
backend daemon picks it up and starts executing.

:::note
You can also approve the backend task by hand from any shell —
`cd ~/code/backend && skep task approve 2`. Either path works. The
plan default is for the sender to call `approve_remote_task` because
the alternative — the human switching tabs mid-flow — is exactly the
friction Skep is built to remove.
:::

**5. Backend executes on its own branch.** Claude in the backend pane
adds the three routes, writes tests, and commits to
`skep/task-2-add-auth-routes`.

**6. Mobile's `wait_remote_task` unblocks.** The moment the backend
hits `done`, `wait_remote_task` returns to the mobile Claude session
with the backend task's result and branch name. The mobile session
has been blocked server-side this whole time — no polling, no token
churn.

**7. Mobile executes the local steps.** Steps 4–8 of the plan.
Claude adds `AuthApi.kt`, updates `NetworkModule.kt` and
`LoginViewModel.kt`, deletes `LegacyAuthApi.kt`, runs
`./gradlew test`, and commits to `skep/task-1-switch-auth-to-backend`.

## Step 5: Review both sides

```bash
skep tasks --all
```

```text
mobile
  #1 switch-auth-to-backend       done   [large]  →  skep/task-1-...   18,420 tok
backend
  #2 add-auth-routes              done   [large]  →  skep/task-2-...   12,180 tok
```

```bash
cd ~/code/backend  && skep task show 2
cd ~/code/mobile   && skep task show 1
```

Each task has its own branch, its own result file, its own token
breakdown.

## Step 6: Merge

The two repos still have completely independent git histories. Skep
has not touched a remote, has not opened a PR, has not merged
anything. Merge each side however you normally do:

```bash
cd ~/code/backend
git checkout main && git merge skep/task-2-add-auth-routes
git push

cd ~/code/mobile
git checkout main && git merge skep/task-1-switch-auth-to-backend
git push
```

Or open two PRs and let CI do its thing. Skep is done.

## What just happened

Three MCP calls did all the cross-repo coordination, and each one has
a specific job:

- **`create_remote_task`** blocks the sender until the peer has
  classified and produced a plan. This is the only step where the
  sender pays for waiting (a few seconds of wall clock). It's worth
  it: the sender's plan now contains the peer task id, which every
  later step needs.
- **`approve_remote_task`** is non-blocking and instant. It exists so
  the sender can drive the peer's lifecycle without the human having
  to switch panes. (You can still approve manually if you want to
  read the peer plan first — see the note above.)
- **`wait_remote_task`** blocks server-side until the peer hits a
  terminal state. This is the long one — minutes, sometimes longer —
  but Skep blocks the *socket call*, not the LLM context. The mobile
  Claude session is paused on a tool result. Zero token cost while
  waiting.

Compare that to doing this by hand: open two Claude sessions, copy
the task description into both, ask backend Claude to do its half,
wait, copy the new route signatures into mobile Claude's context,
hope nothing got lost in the paste, ask mobile Claude to do its
half. Two cold contexts, two re-reads of each repo, and a human
standing between them as the protocol layer.

The Skep version costs roughly the tokens of one cold session with
one extra MCP-roundtrip per delegate step — an illustrative figure,
not a benchmark; see [Benchmarking Skep](/advanced/benchmarking/) for
reproducible numbers — and the human stands at the entrance,
watching the workers waggle the path.
