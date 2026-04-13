---
title: "JetBrains (IntelliJ, GoLand, PyCharm, ...)"
description: A CLI-adjacent workflow built around the JetBrains terminal tool window.
---

There is no Skep JetBrains plugin. Like with VS Code, the JetBrains
integration is a CLI-adjacent workflow built around the IDE's
terminal tool window.

## Setup

1. **Install your IDE.** Any recent JetBrains IDE with the Terminal
   tool window works — GoLand for Go, IntelliJ IDEA for JVM,
   PyCharm for Python, etc.
2. **Install [Claude Code](https://docs.claude.com/en/docs/claude-code)**
   so `claude` is available on `PATH` from the IDE terminal. See
   [Changelog](/changelog/) for the current list of wired backends.
3. **Register the Skep MCP server once, globally:**
   ```bash
   skep mcp install
   ```

## Workflow

- Open the project in your JetBrains IDE.
- Open the **Terminal** tool window (`Alt+F12` on most platforms).
- Run `skep task create`, `skep tasks`, `skep task approve N`,
  etc. directly in that terminal.
- For the cockpit, open an **external terminal** next to the IDE
  and run:
  ```bash
  tmux new -s work
  skep workspace watch
  ```
  Task panes spawned by daemons will land in that external tmux
  session, not inside the IDE's terminal tool window.

## Optional: File Watchers on save

The JetBrains **File Watchers** plugin can trigger `skep index` on
save for extra-paranoid setups where fsnotify isn't reliable:

- *Program:* `skep`
- *Arguments:* `index`
- *File type:* source files only
- *Filter:* exclude `.skep/` and `.git/` so you don't feedback-loop

Most users don't need this — Skep already watches files via
fsnotify.

## Caveats

- No native plugin yet. If you want one, file an issue; the socket
  protocol is stable enough to wrap.
- The IDE terminal is a plain shell, not tmux. The cockpit belongs
  in an external terminal emulator.
