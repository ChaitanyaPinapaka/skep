---
title: VS Code
description: Skep is a CLI; VS Code is an editor with a very good integrated terminal. Here is how to use them together.
---

There is no Skep VS Code extension. You don't need one. Skep is a
CLI; VS Code is an editor with a very good integrated terminal. The
workflow below is how to use them together.

## Setup

1. **Install VS Code.** Use the integrated terminal — no extra
   terminal extension required.
2. **Install your LLM CLI.** If you use Claude Code, install it
   globally and make sure `claude --version` works from the VS Code
   terminal. If a Claude Code VS Code extension exists that suits
   you, install it — Skep doesn't care which frontend you use for
   the actual LLM session, only that the CLI is on `PATH`.
3. **Register the Skep MCP server once, globally:**
   ```bash
   skep mcp install
   ```
   This adds a `skep` entry to `~/.claude/settings.json` and will
   be picked up by Claude Code regardless of how it's launched.

## Workflow

- Open your project in VS Code.
- Open the integrated terminal (`` Ctrl+` ``).
- Run `skep task create "..."`, `skep tasks`, `skep task show N`,
  `skep task approve N` as usual.
- For task execution, the Skep daemon will try to spawn a tmux pane
  in your attached session. VS Code's integrated terminal is **not**
  tmux, so the daemon's pane-spawn won't land there natively.

The pragmatic pattern: keep an **external terminal emulator** open
next to VS Code with your tmux cockpit running (`skep workspace
watch` + the daemon sessions). Use VS Code for editing and its
integrated terminal for read-only commands like `skep index ask` and
`skep tasks`. Let the cockpit handle task panes.

## Optional: keybindings via `tasks.json`

Paste into `.vscode/tasks.json` to bind `skep doctor` and
`skep workspace watch` to the VS Code tasks palette:

```json
{
  "version": "2.0.0",
  "tasks": [
    {
      "label": "skep: doctor",
      "type": "shell",
      "command": "skep doctor",
      "problemMatcher": []
    },
    {
      "label": "skep: workspace watch",
      "type": "shell",
      "command": "skep workspace watch",
      "isBackground": true,
      "problemMatcher": []
    }
  ]
}
```

Then `Ctrl+Shift+P → Tasks: Run Task → skep: doctor`. Bind either
to a keybinding through `keybindings.json` if you want.

## Caveats

- No native tmux integration inside VS Code's terminal. Use an
  external terminal for the cockpit.
- VS Code's auto-save triggers fsnotify — Skep will re-index on
  every save, which is usually what you want, but watch for noisy
  CPU on very large projects.
