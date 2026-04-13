// Package tmuxutil holds small helpers for introspecting and mutating
// tmux state from Go. Owned by multiple packages (cmd/skep, internal/daemon,
// internal/tasks) that all need to map task branches to tmux panes,
// rename windows on watchdog events, and switch focus on jump-pending.
// Keeps the knowledge of tmux command shapes in one place so a tmux
// behavior change is a one-file update.
package tmuxutil

import (
	"os/exec"
	"strings"
)

// FindTargetByBranchName scans all tmux panes across all sessions and
// returns the first one whose window name contains the short form of
// the given task branch (everything after the last `/`).
//
// The match is a substring match on the short slug rather than an
// exact equality because `daemon.SpawnTaskInTmux` may prefix the
// window name (e.g. with `[!]` from the approval watchdog) and the
// `skep task attach` path needs to still find it.
//
// Returns "" when no match is found or when tmux is not reachable —
// callers use that as a "no visible pane" signal and degrade
// gracefully rather than erroring.
func FindTargetByBranchName(branch string) string {
	if branch == "" {
		return ""
	}
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{session_name}:#{window_index}.#{pane_index} #{window_name}").Output()
	if err != nil {
		return ""
	}
	short := branch
	if i := strings.LastIndex(short, "/"); i >= 0 {
		short = short[i+1:]
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.Contains(parts[1], short) {
			return parts[0]
		}
	}
	return ""
}

// SetWindowNamePrefix renames the window of a given target to add or
// strip a managed prefix like "[!] ". Idempotent: passing the same
// prefix twice is a no-op because we query the current name first
// and re-apply.
//
// The target may be a full "session:window.pane" or just "session:window"
// — rename-window operates on windows, so we strip any .pane suffix
// before running the command.
//
// All tmux errors are silently dropped — cosmetic window renaming
// must never crash the daemon's watchdog loop.
func SetWindowNamePrefix(target, prefix string) {
	if target == "" {
		return
	}
	window := target
	if dot := strings.LastIndex(window, "."); dot > 0 {
		window = window[:dot]
	}
	nameOut, err := exec.Command("tmux", "display-message", "-p",
		"-t", window, "#W").Output()
	if err != nil {
		return
	}
	name := strings.TrimSpace(string(nameOut))
	name = stripManagedPrefix(name)
	if prefix != "" {
		name = prefix + " " + name
	}
	_ = exec.Command("tmux", "rename-window", "-t", window, name).Run()
}

// stripManagedPrefix removes a leading "[!] " so SetWindowNamePrefix
// can toggle the prefix without stacking "[!] [!] [!] task-3".
func stripManagedPrefix(name string) string {
	if strings.HasPrefix(name, "[!] ") {
		return strings.TrimPrefix(name, "[!] ")
	}
	return name
}

// CapturePaneTail runs `tmux capture-pane -p -J -t <target> -S -N`
// and returns the captured text. Returns "" (never an error) on any
// tmux failure — the approval watchdog is best-effort.
//
// -J joins wrapped lines so a prompt split across narrow pane widths
// ("Continue? [y/\n]") still matches.
// -p prints to stdout instead of the tmux paste buffer.
// -S -N scrolls back N lines from the bottom.
func CapturePaneTail(target string, lines int) string {
	if target == "" {
		return ""
	}
	if lines <= 0 {
		lines = 30
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-J",
		"-t", target, "-S", negString(lines)).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// negString returns "-N" for a positive N without pulling in fmt
// for one int-to-string conversion on the hot path.
func negString(n int) string {
	if n <= 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	buf = append(buf, '-')
	// decimal conversion
	start := len(buf)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse the digits in place
	for i, j := start, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
