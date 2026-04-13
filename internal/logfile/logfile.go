package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const logRetention = 10 * 24 * time.Hour // 10 days

// LogFile is an append-only log with automatic 10-day retention.
type LogFile struct {
	path string
	mu   sync.Mutex
}

// NewLogFile creates or opens a log file, pruning entries older than 10 days.
func NewLogFile(path string) *LogFile {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return &LogFile{path: path}
	}
	lf := &LogFile{path: path}
	lf.prune()
	return lf
}

// Log writes a timestamped line to the log file.
func (lf *LogFile) Log(format string, args ...interface{}) {
	lf.mu.Lock()
	defer lf.mu.Unlock()

	f, err := os.OpenFile(lf.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "%s  %s\n", time.Now().UTC().Format("2006-01-02 15:04:05"), msg)
}

// Crash writes a panic recovery report with stack trace and context.
func (lf *LogFile) Crash(context string, panicVal interface{}) {
	lf.mu.Lock()
	defer lf.mu.Unlock()

	f, err := os.OpenFile(lf.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	ts := time.Now().UTC().Format("2006-01-02 15:04:05")
	fmt.Fprintf(f, "\n%s  ===== CRASH =====\n", ts)
	fmt.Fprintf(f, "%s  context: %s\n", ts, context)
	fmt.Fprintf(f, "%s  panic: %v\n", ts, panicVal)
	fmt.Fprintf(f, "%s  stack:\n%s\n", ts, debug.Stack())
	fmt.Fprintf(f, "%s  ===== END CRASH =====\n\n", ts)
}

// prune removes lines older than 10 days.
func (lf *LogFile) prune() {
	data, err := os.ReadFile(lf.path)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-logRetention)
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) < 19 {
			continue
		}
		t, err := time.Parse("2006-01-02 15:04:05", line[:19])
		if err != nil {
			kept = append(kept, line) // keep non-timestamped lines (stack traces)
			continue
		}
		if t.After(cutoff) {
			kept = append(kept, line)
		}
	}
	os.WriteFile(lf.path, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

// IndexLog returns the main log file at .skep/index.log
func IndexLog(skepDir string) *LogFile {
	return NewLogFile(filepath.Join(skepDir, "index.log"))
}

// BranchLog returns a branch-specific log file at .skep/branches/<branch>.log
func BranchLog(skepDir, branch string) *LogFile {
	return NewLogFile(filepath.Join(skepDir, "branches", branch+".log"))
}

// RecoverWith returns a deferred recovery function that logs crashes.
//
// Usage:
//
//	defer agent.RecoverWith(log, "task #5 execution")()
func RecoverWith(log *LogFile, context string) func() {
	return func() {
		if r := recover(); r != nil {
			if log != nil {
				log.Crash(context, r)
			}
			fmt.Fprintf(os.Stderr, "skep: panic in %s: %v\n", context, r)
			fmt.Fprintf(os.Stderr, "skep: see %s for stack trace\n", log.path)
		}
	}
}
