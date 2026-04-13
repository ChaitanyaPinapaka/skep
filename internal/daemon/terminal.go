package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Terminal manages spawning Claude TUI sessions in tmux windows.
// Max concurrent windows is configurable (default 2). Overflow is queued.
type Terminal struct {
	skepBin    string // path to skep binary
	layout     string // tmux layout: split-h, split-v, window, popup
	maxConcur  int
	active     atomic.Int32
	mu         sync.Mutex
	queue      []int // task IDs waiting
	onComplete func(taskID int)
}

// NewTerminal creates a terminal manager.
func NewTerminal(maxConcurrent int, layout string, onComplete func(taskID int)) *Terminal {
	bin, _ := os.Executable()
	if layout == "" {
		layout = "split-h"
	}
	return &Terminal{
		skepBin:    bin,
		layout:     layout,
		maxConcur:  maxConcurrent,
		onComplete: onComplete,
	}
}

// Spawn opens a tmux window for a task. If at capacity, queues it.
func (t *Terminal) Spawn(taskID int, taskName string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if int(t.active.Load()) >= t.maxConcur {
		t.queue = append(t.queue, taskID)
		return nil // queued, will run when a slot opens
	}
	return t.launchLocked(taskID, taskName)
}

// launchLocked is launch() called while holding t.mu.
func (t *Terminal) launchLocked(taskID int, taskName string) error {
	return t.launch(taskID, taskName)
}

// QueueLen returns the number of tasks waiting.
func (t *Terminal) QueueLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queue)
}

// ActiveCount returns the number of running tmux windows.
func (t *Terminal) ActiveCount() int {
	return int(t.active.Load())
}

func (t *Terminal) launch(taskID int, taskName string) error {
	t.active.Add(1)

	windowName := fmt.Sprintf("skep-%d-%s", taskID, taskName)
	if len(windowName) > 30 {
		windowName = windowName[:30]
	}

	idStr := fmt.Sprintf("%d", taskID)
	// Shell wrapper: keep window open on exit so user can see final output
	runScript := fmt.Sprintf(
		`%s task run %s --inline; code=$?; echo; echo '--- task finished (exit '$code')---'; echo 'Press Enter to close window.'; read`,
		shellQuote(t.skepBin), idStr,
	)

	mode := detectMode()
	var cmd *exec.Cmd
	switch mode {
	case "tmux-attached":
		cmd = tmuxSpawnCommand(t.layout, windowName, runScript)

	case "tmux-available":
		sessionName := fmt.Sprintf("skep-task-%d", taskID)
		cmd = exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-n", windowName,
			"bash", "-c", runScript)

	default:
		t.active.Add(-1)
		return fmt.Errorf("tmux not available. Run manually: skep task run %d", taskID)
	}

	if err := cmd.Run(); err != nil {
		t.active.Add(-1)
		return fmt.Errorf("launch tmux window: %w", err)
	}

	// Monitor the tmux window — when it closes, dequeue next task
	go t.monitor(taskID, windowName, mode)

	return nil
}

// monitor waits for a tmux window to close, then dequeues the next task.
func (t *Terminal) monitor(taskID int, windowName, mode string) {
	// Poll for tmux window existence
	for {
		var exists bool
		switch mode {
		case "tmux-attached":
			out, _ := exec.Command("tmux", "list-windows", "-F", "#{window_name}").Output()
			exists = strings.Contains(string(out), windowName)
		case "tmux-available":
			sessionName := fmt.Sprintf("skep-task-%d", taskID)
			err := exec.Command("tmux", "has-session", "-t", sessionName).Run()
			exists = err == nil
		}

		if !exists {
			break
		}

		// Check every 2 seconds
		time.Sleep(2 * time.Second)
	}

	t.active.Add(-1)

	if t.onComplete != nil {
		t.onComplete(taskID)
	}

	// Dequeue next task if any
	t.mu.Lock()
	if len(t.queue) > 0 {
		nextID := t.queue[0]
		t.queue = t.queue[1:]
		t.mu.Unlock()
		t.launch(nextID, fmt.Sprintf("task-%d", nextID))
	} else {
		t.mu.Unlock()
	}
}

// tmuxSpawnCommand builds the tmux command for the given layout.
// Targets the currently attached tmux session if there is one — so the task pane
// appears next to the user's active work, not in the daemon's hidden session.
// Falls back to the daemon's own session if no client is attached.
func tmuxSpawnCommand(layout, windowName, runScript string) *exec.Cmd {
	// Wrap the run script so it sets the pane title to windowName before execution.
	titledScript := fmt.Sprintf("printf '\\033]2;%%s\\033\\\\' %s; %s", shellQuote(windowName), runScript)

	// Find the most recently active attached tmux client's session.
	// If none, split/window goes to the current session (daemon's).
	target := attachedSession()

	switch layout {
	case "split-v":
		args := []string{"split-window", "-v"}
		if target != "" {
			args = append(args, "-t", target)
		}
		args = append(args, "bash", "-c", titledScript)
		return exec.Command("tmux", args...)

	case "window":
		args := []string{"new-window", "-n", windowName}
		if target != "" {
			args = append(args, "-t", target)
		}
		args = append(args, "bash", "-c", titledScript)
		return exec.Command("tmux", args...)

	case "popup":
		// Popups are attached to the current client; no -t support
		return exec.Command("tmux", "display-popup", "-E", "-w", "80%", "-h", "80%",
			"-T", windowName, "bash -c "+shellQuote(titledScript))

	default: // split-h
		args := []string{"split-window", "-h"}
		if target != "" {
			args = append(args, "-t", target)
		}
		args = append(args, "bash", "-c", titledScript)
		return exec.Command("tmux", args...)
	}
}

// attachedSession returns the name of the most recently active attached tmux
// client's session, or "" if no client is attached.
// This lets the daemon spawn task panes next to the user's actual work rather
// than in a hidden detached session.
func attachedSession() string {
	cmd := exec.Command("tmux", "list-clients", "-F",
		"#{session_name}|#{client_activity}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	var bestName string
	var bestActivity int64
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		name := parts[0]
		var activity int64
		fmt.Sscanf(parts[1], "%d", &activity)
		if activity > bestActivity {
			bestActivity = activity
			bestName = name
		}
	}

	if bestName == "" {
		return ""
	}
	// Target the session — tmux will use its active window/pane
	return bestName + ":"
}

// detectMode checks if tmux is available and if we're inside a session.
func detectMode() string {
	if os.Getenv("TMUX") != "" {
		return "tmux-attached"
	}
	if _, err := exec.LookPath("tmux"); err == nil {
		return "tmux-available"
	}
	return "none"
}

// SpawnTaskInTmux launches `skep task run <id> --inline` in a tmux view.
// layout controls how tmux opens Claude when inside a tmux session:
//
//	split-h → vertical split (pane on the right, default)
//	split-v → horizontal split (pane at the bottom)
//	window  → new tmux window (fullscreen)
//	popup   → floating popup window (tmux ≥3.2)
//
// When not inside tmux, creates a new session regardless of layout.
func SpawnTaskInTmux(taskID int, taskName, layout string) error {
	skepBin, err := os.Executable()
	if err != nil {
		skepBin = "skep"
	}

	windowName := fmt.Sprintf("skep-%d-%s", taskID, taskName)
	if len(windowName) > 30 {
		windowName = windowName[:30]
	}

	// Shell wrapper: run the task, then keep the window open on exit.
	// User presses Enter or kills the window when ready.
	idStr := fmt.Sprintf("%d", taskID)
	runScript := fmt.Sprintf(
		`%s task run %s --inline; code=$?; echo; echo '--- task finished (exit '$code')---'; echo 'Press Enter to close window.'; read`,
		shellQuote(skepBin), idStr,
	)

	mode := detectMode()
	switch mode {
	case "tmux-attached":
		cmd := tmuxSpawnCommand(layout, windowName, runScript)
		return cmd.Run()

	case "tmux-available":
		sessionName := fmt.Sprintf("skep-task-%d", taskID)
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
		create := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-n", windowName,
			"bash", "-c", runScript)
		if err := create.Run(); err != nil {
			return fmt.Errorf("tmux new-session: %w", err)
		}
		attach := exec.Command("tmux", "attach-session", "-t", sessionName)
		attach.Stdin = os.Stdin
		attach.Stdout = os.Stdout
		attach.Stderr = os.Stderr
		return attach.Run()

	default:
		cmd := exec.Command(skepBin, "task", "run", idStr, "--inline")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
}

// shellQuote wraps a string in single quotes for safe shell use.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
