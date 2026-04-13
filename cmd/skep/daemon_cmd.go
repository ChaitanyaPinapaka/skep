package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/daemon"
)

// cmdDaemon dispatches `skep daemon [verb]`.
// Verbs: start (default), stop, status.
// Bare `skep daemon` runs the daemon in the foreground (the common case).
func cmdDaemon(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	rdir := filepath.Join(root, ".skep")

	verb := "start"
	if len(args) > 0 {
		switch args[0] {
		case "start", "stop", "status":
			verb = args[0]
		}
	}

	switch verb {
	case "stop":
		if !daemon.IsRunning(rdir) {
			fmt.Println("daemon not running")
			return nil
		}
		resp, err := daemon.Send(rdir, daemon.Request{Cmd: "stop"})
		if err != nil {
			return err
		}
		if resp.OK {
			fmt.Println("daemon stopped")
		}
		return nil
	case "status":
		if !daemon.IsRunning(rdir) {
			fmt.Println("daemon not running")
			return nil
		}
		pid := daemon.ReadPID(rdir)
		fmt.Printf("daemon running (pid %d)\n", pid)
		if target := findTmuxTargetForPID(pid); target != "" {
			fmt.Printf("tmux:   %s\n", target)
		} else {
			fmt.Println("tmux:   not attached to a tmux pane")
		}
		fmt.Printf("socket: %s\n", filepath.Join(rdir, "agent.sock"))
		return nil
	}

	// start — foreground daemon, user backgrounds with & or systemd.
	return daemon.Run(root, rdir)
}

// findTmuxTargetForPID walks up the process tree from pid until it finds an
// ancestor that matches a tmux pane_pid. Returns the pane target in
// "session:window.pane" form, or "" if not found (or on non-Linux).
func findTmuxTargetForPID(pid int) string {
	if pid <= 0 {
		return ""
	}

	// Build a map of tmux pane_pid → target.
	cmd := exec.Command("tmux", "list-panes", "-a",
		"-F", "#{pane_pid}|#{session_name}:#{window_index}.#{pane_index}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	panePIDs := make(map[int]string)
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		p, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		panePIDs[p] = parts[1]
	}
	if len(panePIDs) == 0 {
		return ""
	}

	// Walk ancestors. Cap at 20 hops to avoid pathological loops.
	cur := pid
	for i := 0; i < 20 && cur > 1; i++ {
		if target, ok := panePIDs[cur]; ok {
			return target
		}
		ppid := readPPID(cur)
		if ppid == 0 || ppid == cur {
			break
		}
		cur = ppid
	}
	return ""
}

// readPPID returns the parent PID of pid by reading /proc/<pid>/stat.
// Returns 0 on error or on non-Linux systems.
func readPPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// Format: pid (comm) state ppid ...
	// comm may contain spaces and parens, so find the LAST ')' and parse from there.
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+1 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[idx+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
