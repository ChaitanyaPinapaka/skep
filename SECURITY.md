# Security

Skep is a local developer tool — it runs a daemon per repo, spawns
Claude Code sessions in tmux panes, and serves an MCP server for
cross-repo coordination. The threat model is focused on local
exploitation and committed-config injection, not network attacks.

## Reporting a vulnerability

**Please do not file a public GitHub issue for security reports.**

Use GitHub's private vulnerability reporting instead:

1. Go to [github.com/ChaitanyaPinapaka/skep/security/advisories/new](https://github.com/ChaitanyaPinapaka/skep/security/advisories/new)
2. Describe the issue, the affected version(s), and a minimal
   reproducer when possible.
3. I will acknowledge within **7 days** and keep you posted on the
   fix cadence.

If GitHub's private reporting is unavailable to you for any reason,
you can open a blank issue titled `security: requesting private
channel` with no details, and I will respond with an alternative
path.

## Scope

The following **are** in scope and will be treated as security
issues:

- Local privilege-escalation paths in the daemon (e.g. non-root
  local user injecting tasks into another user's daemon).
- Command injection via `.skep/config.json`, task descriptions,
  plan text, or peer-socket messages.
- Cross-repo task injection that bypasses dedup or peer validation.
- Path traversal in any file-reading code path (index builder,
  executor result writer, clarify file reader).
- Arbitrary code execution via a crafted `test_cmd` after bypassing
  the out-of-repo trust store.
- MCP tool vulnerabilities — any shape of input to a tool handler
  that produces unintended side effects.

The following **are not** in scope:

- The LLM producing insecure, broken, or incorrect code. Skep does
  not verify LLM output; that's the user's job, and the reason
  large and ambiguous tasks gate on explicit human approval.
- Bugs in tools Skep shells out to (Claude Code, tmux, git) — please
  report those upstream.
- Configuration that the user can change: a user who explicitly
  sets `test_cmd` to a dangerous command and approves it has the
  keys to their own machine.

## Past advisories

None yet. v0.1.0 is the first public release. This file will be
updated with a CHANGELOG-style entry when the first fix ships.
