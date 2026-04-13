---
title: The workspace cockpit
description: A single tmux session that gives you live visibility into every task across every repo.
---

The **cockpit** is a single tmux session you keep open while Skep
works. Your shell lives on the left. Task windows stack on the right —
one per **forager**, Skep's bee-metaphor name for a single repo's
running daemon + its Claude session. The status line at the bottom of
the tmux session pulls live state from every forager so you can see
what every agent is doing without leaving your shell.

## The layout

```
┌───────────────────┬────────────────────────────┐
│ shell (main)      │ window 1: task-3 claude    │
│                   │                             │
│ $ skep task       │ ... claude editing files    │
│   create "..."    │                             │
│                   │                             │
│                   │ [task-2] [task-3*][task-5] │
└───────────────────┴────────────────────────────┘
 ◠ 2 exec · 1 pending · 🔔 #5 waiting approval    14:23
```

The bottom line is real. It comes from `skep status --oneline`, which
tmux polls every few seconds and renders in its own status bar. You
get ambient awareness without giving up a permanent pane.

## One-command setup

Run this once:

```bash
skep cockpit setup
```

It writes `~/.tmux.conf.d/skep.conf` with every keybinding, mouse,
scrollback, status-line, and popup setting Skep needs. Your own
`~/.tmux.conf` is untouched. You own that file. Skep only owns its
own drop-in.

Add a single line to `~/.tmux.conf`:

```text
source-file ~/.tmux.conf.d/skep.conf
```

Reload without restarting tmux:

```bash
tmux source-file ~/.tmux.conf
```

You are now flying the cockpit.

## Keybindings

All bindings use the default tmux prefix `Ctrl+b`.

| Binding | Action |
|---|---|
| `Ctrl+b h/j/k/l` | Move between panes, vim style |
| `Ctrl+b H/J/K/L` | Resize the current pane, repeatable |
| `Ctrl+b |` / `-` | Split current pane (keeps cwd) |
| `Ctrl+b c` | New window in current cwd |
| `Ctrl+b 1..9` | Jump to task window by number |
| `Ctrl+b S` | Pop up the full apiary (90% x 80%) |
| `Ctrl+b T` | Pop up `skep tasks` |
| `Ctrl+b A` | Pop up `skep index ask` |
| `Ctrl+b !` | Jump to the oldest task waiting for approval |
| `Ctrl+b R` | Reload the tmux config |
| `Ctrl+b [` | Enter copy mode to scroll Claude's output |
| `Ctrl+b [` then `v`, `y` | Vim-style select + yank in copy mode |

The apiary popup is on demand. Hit `Ctrl+b S` when you want the full
cross-repo picture, and it disappears again when you are done. No
permanent real estate.

## What `skep.conf` sets and why

`skep cockpit setup` writes a curated set of tmux options — not
because Skep needs them, but because the cockpit experience falls
apart without them. Each setting below earns its place in the file.
If you already have a tmux config you love, you can cherry-pick from
this list instead of sourcing the whole drop-in.

### Responsiveness

| Option | Why |
|---|---|
| `set -sg escape-time 0` | tmux's default 500 ms swallows the `Esc` key, which makes vim and the Claude Code TUI feel laggy. Zero restores instant escape. |
| `set -g focus-events on` | Forwards focus in/out events to running apps. Vim autoread fires, Claude Code repaints, task panes don't go stale when you click away. |
| `set -g default-terminal "tmux-256color"` | 256-color profile so Claude's syntax highlighting lands in the right palette. |
| `set -ga terminal-overrides ",*256col*:Tc,xterm-256color:Tc,alacritty:Tc"` | Passes truecolor (24-bit) through tmux to the inner terminal. Without this, Claude Code falls back to 16 colors. |

### Window and pane indices

| Option | Why |
|---|---|
| `set -g base-index 1` | Windows start at 1 instead of 0 so `Ctrl+b 1..9` matches the number row on your keyboard. |
| `setw -g pane-base-index 1` | Same for panes. |
| `set -g renumber-windows on` | When a window closes, the rest slide down. Keeps `Ctrl+b 1..9` pointing at what you expect after a task finishes. |
| `set -g detach-on-destroy off` | Don't kill the session when the last window exits. The cockpit is a home you keep coming back to, not a one-shot. |
| `setw -g aggressive-resize on` | When a smaller client attaches temporarily (laptop detaches and reattaches, phone ssh), don't permanently shrink your cockpit panes to match. |

### Scrolling and mouse

| Option | Why |
|---|---|
| `set -g mouse on` | Lets you scroll a pane with the wheel. Without this, the wheel does nothing inside a pane and Claude's output is unreadable after 40 lines. |
| `set -g history-limit 50000` | Default 2000 lines runs out on a 3-minute Claude edit session. 50k is enough to see a whole task's output without paying real memory cost. |

### Copy mode (vim keys)

| Option | Why |
|---|---|
| `setw -g mode-keys vi` | Consistent muscle memory with your editor — `h/j/k/l` to move, `v` to start selection, `y` to yank. |
| `bind -T copy-mode-vi v send -X begin-selection` | Visual selection. |
| `bind -T copy-mode-vi y send -X copy-pipe-and-cancel` | Yank to tmux's buffer and exit copy mode in one keystroke. Paste back with `Ctrl+b ]`. |
| `bind -T copy-mode-vi MouseDragEnd1Pane send -X copy-pipe-no-clear` | Dragging with the mouse selects but does *not* clear the moment you release, which matches how most GUIs behave. |

### Pane navigation

| Option | Why |
|---|---|
| `bind h/j/k/l select-pane` | Vim-direction pane jumps. |
| `bind -r H/J/K/L resize-pane` | Hold to resize. `-r` lets you hold the key without re-pressing prefix. |
| `bind | split-window -h -c "#{pane_current_path}"` | Intuitive split key *and* the new pane starts in the same directory. |
| `bind - split-window -v -c "#{pane_current_path}"` | Same for horizontal splits. |
| `bind c new-window -c "#{pane_current_path}"` | New windows inherit cwd so you're not stuck `cd`-ing every time. |

### Cockpit popups and jumps

| Option | Why |
|---|---|
| `bind S display-popup -w 90% -h 80% -E 'skep workspace watch'` | The full apiary on demand. Dismissible with `q`. Beats a permanent dashboard pane. |
| `bind T display-popup -w 80% -h 50% -E 'skep tasks'` | Quick list of the current repo's tasks. |
| `bind A display-popup -w 70% -h 60% -E 'skep index ask'` | Pop up `skep index ask` so you can search the index mid-task without losing your current pane. |
| `bind ! run-shell 'skep task jump-pending'` | Zero-click jump to the oldest task flagged by the approval watchdog. |
| `bind R source-file ~/.tmux.conf` | One-touch reload while tuning the cockpit. |

### Activity and status line

| Option | Why |
|---|---|
| `setw -g monitor-activity on` | Tmux flags a window name when a background pane produces output. Combined with the watchdog, you can tell idle foragers from working ones at a glance. |
| `set -g visual-activity off` | Don't blink the whole status line on every keystroke. |
| `set -g visual-bell off` + `set -g bell-action any` | Prefer the watchdog's explicit `[!]` prefix over tmux's generic bell flash. |
| `set -g status-interval 5` | Refresh the status line every 5 seconds — `#(skep status --oneline)` reruns on each tick. |
| `set -g status-right '#(skep status --oneline) %H:%M'` | The actual cockpit dashboard. `◠ 2 exec · 1 pending · 🔔 #5 waiting approval` lives here permanently so you never need a dashboard pane. |

### Styling

The defaults in `skep.conf` use muted `colour234/238/244` for the status
bar, pane borders, and messages so the cockpit reads on both dark and
light terminal themes. If you already have a tmux theme you love,
comment out the `status-style`, `window-status-current-style`,
`message-style`, and `*border-style` lines — everything else still
works without them.

## The approval watchdog

Claude Code sometimes pauses a run to ask *Do you want to proceed?*
In a background pane, that prompt is invisible and the forager just
sits there quietly until you happen to look.

Skep watches for this. The daemon tails the output of every task pane
and matches against a short list of approval patterns. When a match
fires, Skep:

1. Flags the task with `NeedsInput` in the database.
2. Prefixes the tmux window name with `[!]` so you see it in the
   window bar.
3. Surfaces it in `skep status --oneline`, which lights up your tmux
   status line with `🔔 #N waiting approval`.

Respond by pressing `Ctrl+b !`. Skep jumps you straight to the oldest
waiting pane so you can answer the prompt in place. Under the hood
this runs `skep task jump-pending`, which you can also invoke from
any shell.

See [Troubleshooting](/advanced/troubleshooting/) if a prompt slips
past the watchdog.

## Pane vs window layout

Each approved task spawns either a new pane or a new window. Windows
are the default for a multi-task cockpit because they keep each
forager full-screen and navigable by number. Choose your layout:

```bash
skep config tmux-layout window     # new window per task (default)
skep config tmux-layout split-h    # vertical split
skep config tmux-layout split-v    # horizontal split
skep config tmux-layout popup      # floating popup (tmux >= 3.2)
```

Mix and match per repo. The daemon reads this value when it is about
to spawn a forager.

## Running a daemon in the background

The daemon is a separate concern. Start one per repo, detached from
your cockpit:

```bash
cd ~/code/backend && skep daemon &
```

Or run it in its own named tmux session and forget about it. See
[Install and first run](/getting-started/install/) for the full
daemon walkthrough. `skep daemon status` reports pid, socket, and the
tmux target it will spawn foragers into.

## When you don't need the daemon

`skep index ask`, `skep status`, `skep tasks`, `skep workspace`, and
`skep index` all read straight from the SQLite index. You only need
the daemon for automatic classification, background execution, and
cross-repo routing.
