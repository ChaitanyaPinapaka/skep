---
title: "Troubleshooting"
description: "Common problems and how to fix them. Start with `skep doctor`."
---

When something isn't working, run `skep doctor` first. It is a fast
health check of every runtime dependency Skep relies on, and most
problems show up there before you have to dig anywhere else.

## `skep doctor` output explained

| Check | What it means | Fix |
|---|---|---|
| `claude` (Claude Code CLI) | The Claude binary is on `PATH` | Install from [docs.claude.com/en/docs/claude-code](https://docs.claude.com/en/docs/claude-code) |
| `git` | `git` binary is on `PATH` | Install git (warning only — Skep falls back to filesystem walk) |
| `tmux` | `tmux` binary is on `PATH` | `apt install tmux` / `brew install tmux` |
| `universal-ctags` | `ctags` is available | `apt install universal-ctags` / `brew install universal-ctags` (warning only — needed for Rust/Ruby/Swift symbol extraction) |
| `.skep` dir | This repo has been initialized | `skep init` |
| workspace | A `.skep-workspace/` registry exists somewhere up the tree | `skep init` (creates one on first run if none is found) |

Exit code is 0 when everything green, 1 when a critical dependency
(Claude, tmux) is missing.

## Common problems

### 1. "daemon already running (pid N)"

Another Skep daemon is holding the per-repo flock.

**Diagnose:** `skep daemon status` shows the live pid and the tmux
pane it's bound to.

**Fix:** `skep daemon stop` to shut it down cleanly. If the previous
daemon crashed and left a stale pid file, `skep daemon stop` will
detect that and clear it.

### 2. Task stuck in `executing` after the daemon died

The daemon's startup sweep handles this automatically: on every fresh
boot it scans for `executing` tasks left behind by a previous run and
moves them to `interrupted`, where you can re-run them.

**Diagnose:** if a task is still pinned to `executing` after a daemon
restart, check `.skep/index.log` for the sweep step. It should
mention how many tasks it cleaned up.

**Fix:** start the daemon manually with `skep daemon start` and watch
the log. If the sweep is silently failing, file an issue with the log
attached.

### 3. Classifier returns no plan

Most common cause: the LLM returned a narrative paragraph instead of
the JSON shape Skep expects.

**Diagnose:** `skep task show <id>` — look at the `result` field for
something like `classify failed: parse error` or empty plan steps.

**Fix:** re-create the task with a more action-oriented description.
Phrases like *explain*, *understand*, *research*, *think about* lead
Claude to write prose instead of a plan. Use *add*, *fix*, *rename*,
*delete*, *update*, *implement* — the imperative voice produces a
plan reliably.

### 4. Task runs but does nothing

Claude opens, sits for a moment, makes no edits, exits.

**Diagnose:** check the Claude session JSONL for the task run — it's
under `~/.claude/projects/<encoded-repo-path>/<uuid>.jsonl`. The last
few entries usually explain it. Common cases: the plan was too vague
("improve the code"), the plan referenced files that no longer exist,
or Claude couldn't find the symbols mentioned in the plan.

**Fix:** edit the task description to be more concrete, then delete
and re-create. If the symbol references are stale, run
`skep index create` to refresh the index first.

### 5. Cross-repo task times out

The sender's `wait_remote_task` hit its timeout (default 1800 s).

**Diagnose:** in the peer repo, run `skep tasks` — is the peer task
still in `pending`? If so it is waiting for human approval that never
came.

**Fix:** approve the peer task manually
(`cd <peer> && skep task approve <id>`) and re-run the sender task.
For trivial delegated tasks, set `skep config auto-execute-small true`
on the peer so small delegations don't block on a human.

### 6. Tmux pane won't spawn

The daemon tried to `tmux split-window` and failed.

**Diagnose:** `skep daemon status` shows which tmux session the daemon
is bound to. If that field is empty or says "no tmux", the daemon was
started outside a tmux context and has nowhere to put new panes.

**Fix:** always start the daemon from inside a tmux session:

```bash
tmux new -s work
cd ~/code/your-repo
skep daemon &
```

Or use the hidden-daemon pattern from
[Cockpit setup](/getting-started/cockpit/), which puts the daemon in
its own `skep-<reponame>` session that targets your attached cockpit.

### 7. Can't scroll back through Claude output in a task pane

Tmux owns the scrollback buffer inside each pane — your terminal's
mouse wheel doesn't scroll it by default, and the default 2000-line
buffer fills up fast on a long Claude run.

**Fix:** add two lines to `~/.tmux.conf`:

```text
set -g mouse on
set -g history-limit 50000
```

Reload: `tmux source-file ~/.tmux.conf`.

Without the mouse setting you can still scroll via copy mode:
`Ctrl+b` then `[`, then `PgUp`/`PgDn`, `q` to exit. See
[Cockpit → What `skep.conf` sets and why](/getting-started/cockpit/#what-skepconf-sets-and-why).

### 8. Missed a "Do you want to proceed?" prompt in a task pane

Claude Code occasionally pauses a run to ask for confirmation. In a
background window that prompt is silent and the forager sits waiting
until you notice.

Skep's **approval watchdog** is supposed to catch this. The daemon
tails each task pane, matches against a list of known approval
patterns, flags the task with `NeedsInput`, prefixes the tmux window
name with `[!]`, and surfaces the alert in `skep status --oneline`
(which feeds the cockpit status line).

**Diagnose:** run `skep status --oneline`. A bell (`🔔`) and a task
id mean the watchdog saw the prompt. If you hit a prompt the watchdog
missed, `skep config list` will show the current `approval_patterns`
entry. The default list covers the common Claude Code phrasings but
it is extensible.

**Fix (immediate):** press `Ctrl+b !` from anywhere in the cockpit.
Skep jumps you to the oldest pane waiting on input. You can also run
`skep task jump-pending` from a shell.

**Fix (permanent):** add the phrase you saw to `approval_patterns`
in `.skep/config.json`:

```json
{
  "approval_patterns": [
    "Do you want to proceed\\?",
    "Continue\\? \\(y/n\\)",
    "Your new regex here"
  ]
}
```

Patterns are Go regexps, matched against the tail of each task pane.
Reload by restarting the daemon: `skep daemon stop && skep daemon &`.

If the watchdog is off entirely, set `approval_watchdog: true` in
`.skep/config.json`. See
[Cockpit setup → The approval watchdog](/getting-started/cockpit/#the-approval-watchdog)
for the full story.

### 9. "unknown task verb 'lsit'"

Typo. Skep's `task` dispatcher deliberately rejects unknown verbs to
avoid silently creating a task literally named `lsit`.

**Fix:** `skep help task` lists the valid verbs: `create`, `list`,
`show`, `run`, `attach`, `approve`, `reject`, `clarify`, `jump-pending`,
`done`, `delete`.

### 10. "task #N not found"

Either the task was deleted or you are running Skep in a different
workspace than you think.

**Diagnose:** `skep workspace list` shows the registered repos in the
current workspace. `skep tasks` shows live tasks in the current repo.
If the task you expected is missing from both, it was deleted; if the
workspace listing surprises you, you are in the wrong directory.

**Fix:** `cd` into the right repo and try again. `skep` walks up from
the current directory to find `.skep-workspace/` exactly the way `git`
finds `.git/`.

## Where to find logs

- **Daemon and index log** — `.skep/index.log`. One file per repo,
  appended to across daemon runs. Contains the classify/plan
  pipeline decisions, watchdog state changes, and file-watcher events.
- **Task branch logs** — `.skep/branches/<branch-name>.log`. The
  output of the executor's side-processes (test commands, git
  operations) on a per-branch basis.
- **Dedup log** — `.skep/log/dedup.log`. Plain-text, one line per
  dedup layer invocation. See the `LogDedup` helper in
  `internal/tasks/dedup_log.go` for the field shape.
- **Task result files** — `.skep/task-<id>-result.md`. Generated
  after the task hits a terminal state. Contains the diff and the
  test output summary.
- **Claude session JSONLs** — `~/.claude/projects/<encoded-repo-path>/<uuid>.jsonl`.
  Owned by Claude Code, but Skep records the UUID on the task row so
  you can find the right one.

## Crash recovery

When the daemon starts, it scans `.skep/logs/` for logfile markers
left behind by previously-executing tasks. A marker is a small file
written at the start of each task run that records the task id, the
branch, and the Claude session UUID (when available). If the daemon
finds a marker with no matching `done`/`failed`/`interrupted`
terminal record, it:

1. Marks the task as `interrupted` in the database.
2. Writes a short result file explaining that the previous daemon
   crashed or was killed mid-task.
3. Leaves the branch intact so you can resume with
   `skep task run <id>`, which replays the plan against the same
   Claude session id when one is available.

You never lose a task to a daemon crash — worst case, the branch is
still there and a single `skep task run <id>` picks up where the
previous run stopped. See `internal/logfile/logfile.go` for the
marker format.

## Still stuck?

File an issue at
[github.com/ChaitanyaPinapaka/skep/issues](https://github.com/ChaitanyaPinapaka/skep/issues)
with the output of `skep doctor --json`, the task id, and the daemon
log for that run if you have one.
