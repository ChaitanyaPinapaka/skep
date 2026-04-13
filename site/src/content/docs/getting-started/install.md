---
title: Install
description: Skep runs on Linux, macOS, and WSL2 as a single static binary.
---

Skep runs on Linux, macOS, and WSL2. It is distributed as a single
static binary — nothing to configure, no runtime to install.

## One-line install (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/ChaitanyaPinapaka/skep/main/install.sh | sh
```

The script detects your OS and architecture, downloads the matching
binary from the latest GitHub release, and installs it to
`/usr/local/bin/skep`. Re-run it any time to upgrade.

## Manual download

Grab the archive for your platform from
[the releases page](https://github.com/ChaitanyaPinapaka/skep/releases),
extract it, and put the `skep` binary somewhere on your `PATH`:

```bash
tar -xzf skep_<version>_linux_amd64.tar.gz
sudo mv skep /usr/local/bin/
skep --version
```

## `go install`

If you already have Go 1.21+:

```bash
go install github.com/ChaitanyaPinapaka/skep/cmd/skep@latest
```

This compiles from source. No extra flags required — Skep uses pure-Go
SQLite (`modernc.org/sqlite`) and tree-sitter bindings that bundle their
own C code.

## Verify with `skep doctor`

After installing, run the built-in health check:

```bash
skep doctor
```

`doctor` inspects your environment and reports on each dependency:

- `skep` binary version
- `claude` (Claude Code CLI — the currently supported LLM backend; see [Changelog](/changelog/))
- `tmux` version (needed for the cockpit and task panes)
- `git` (optional — enables fast `git ls-files` indexing)
- Workspace registry + daemon status for the current repo

If any required piece is missing, `doctor` tells you exactly what to
install and where.

## Common issues

**`tmux: command not found`** — install it with `sudo apt install tmux`
on Debian/Ubuntu/WSL2, or `brew install tmux` on macOS. The cockpit,
daemon, and task execution all require tmux.

**`claude: command not found`** — install
[Claude Code](https://docs.claude.com/en/docs/claude-code). Claude
Code is the currently supported LLM backend; see [Changelog](/changelog/)
for planned additions.

**`permission denied: /usr/local/bin/skep`** — the install script uses
`sudo` only if the target directory is not writable. Re-run with
`sudo sh` or install to `~/.local/bin` instead.

## Uninstall

Skep stores per-repo state in `.skep/` and per-workspace state in
`.skep-workspace/`. Global state lives in `~/.skep/` (the trusted-repos
store). The binary itself is a single file.

```bash
# 1. Stop any running daemons
for repo in $(skep workspace list --json | jq -r '.agents[].path'); do
  (cd "$repo" && skep daemon stop 2>/dev/null || true)
done

# 2. Remove the cockpit tmux config
skep cockpit reset

# 3. Remove the binary
sudo rm /usr/local/bin/skep          # or wherever you installed it

# 4. Remove global skep state (trusted-repos store)
rm -rf ~/.skep

# 5. (optional) Remove per-repo state — only if you're done with Skep
find ~/code -type d -name '.skep' -prune -exec rm -rf {} +
find ~/code -type d -name '.skep-workspace' -prune -exec rm -rf {} +
```

Per-repo `.skep/` directories are safe to leave in place — they're
just SQLite databases and config files. Deleting them only destroys
Skep's memory of that repo's tasks and index. Your source code, git
history, and branches are untouched.
