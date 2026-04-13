package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// AcquireLock tries to get an exclusive flock on .skep/daemon.lock.
// Returns the lock file (keep open for lifetime of daemon) or error if already locked.
func AcquireLock(skepDir string) (*os.File, error) {
	lockPath := filepath.Join(skepDir, "daemon.lock")

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		// Another daemon holds the lock — read PID for a useful error
		pid := readPID(skepDir)
		if pid > 0 && isSkepProcess(pid) {
			return nil, fmt.Errorf("daemon already running (pid %d)", pid)
		}
		// Stale lock — the process is gone. Remove and retry.
		os.Remove(lockPath)
		removePID(skepDir)
		f, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open lock (retry): %w", err)
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock (retry): %w", err)
		}
	}

	return f, nil
}

// WritePID writes the current process PID to .skep/daemon.pid.
func WritePID(skepDir string) {
	os.WriteFile(filepath.Join(skepDir, "daemon.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// ReadPID reads the daemon PID. Returns 0 if not found.
func ReadPID(skepDir string) int {
	return readPID(skepDir)
}

func readPID(skepDir string) int {
	data, err := os.ReadFile(filepath.Join(skepDir, "daemon.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func removePID(skepDir string) {
	os.Remove(filepath.Join(skepDir, "daemon.pid"))
}

// isSkepProcess checks if a PID belongs to a skep process via /proc/cmdline.
func isSkepProcess(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// /proc not available (macOS) — fall back to kill(0)
		proc, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		return proc.Signal(syscall.Signal(0)) == nil
	}
	return strings.Contains(string(data), "skep")
}

// Cleanup removes PID and lock files.
func Cleanup(skepDir string) {
	removePID(skepDir)
	os.Remove(filepath.Join(skepDir, "daemon.lock"))
}
