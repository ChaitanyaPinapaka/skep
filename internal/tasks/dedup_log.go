package tasks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DedupLogEntry is one line in the dedup observability log.
//
// Every dedup check — fast path and LLM — appends one entry so that the
// question "how often does the LLM layer actually catch something the
// cheap layers missed?" can be answered by grep against a plain text
// file. No structured/JSONL format: plain text so humans can tail -f
// it during development and grep with standard tools.
type DedupLogEntry struct {
	Layer       string        // "keyword", "trigram", "tfidf", "minhash", "llm"
	Hit         bool          // true if this layer flagged a duplicate
	Score       float64       // layer-specific similarity score (0..1 where meaningful)
	TaskDesc    string        // new task description (truncated)
	CandidateID int           // matched task id, 0 if miss
	Duration    time.Duration // wall clock of this layer's check
	Err         error         // non-nil if the layer errored (still logged; advisory semantics)
	Reason      string        // human-readable explanation from the layer
}

// dedupLogMu serializes writes to the log file. The log path is opened
// once per call (O_APPEND is atomic for small writes on Linux, but we
// keep the mutex as belt-and-braces for Windows WSL filesystems which
// do not always honor O_APPEND atomicity).
var dedupLogMu sync.Mutex

// LogDedup appends a single dedup event to .skep/log/dedup.log. Errors
// writing the log are intentionally dropped — observability must never
// block task creation. Callers pass repoRoot (the .skep parent); the
// log directory is created on demand.
func LogDedup(repoRoot string, entry DedupLogEntry) {
	if repoRoot == "" {
		return
	}
	dedupLogMu.Lock()
	defer dedupLogMu.Unlock()

	logDir := filepath.Join(repoRoot, ".skep", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	logPath := filepath.Join(logDir, "dedup.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	// Format: RFC3339 layer=x hit=bool score=0.00 ms=N cand=N err=... reason="..." task="..."
	// Stable field order so awk/cut/grep one-liners keep working.
	fmt.Fprintf(f, "%s layer=%s hit=%t score=%.3f ms=%d cand=%d",
		time.Now().UTC().Format(time.RFC3339),
		entry.Layer,
		entry.Hit,
		entry.Score,
		entry.Duration.Milliseconds(),
		entry.CandidateID,
	)
	if entry.Err != nil {
		fmt.Fprintf(f, " err=%q", oneLine(entry.Err.Error(), 200))
	}
	if entry.Reason != "" {
		fmt.Fprintf(f, " reason=%q", oneLine(entry.Reason, 200))
	}
	if entry.TaskDesc != "" {
		fmt.Fprintf(f, " task=%q", oneLine(entry.TaskDesc, 160))
	}
	fmt.Fprintln(f)
}

// oneLine collapses newlines/tabs and caps length so a single dedup
// entry cannot span multiple log lines (which would break grep).
func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.TrimSpace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
