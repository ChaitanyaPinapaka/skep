// Package progress provides terminal-aware progress output.
// Silent when stderr is not a terminal (e.g., piped, redirected).
package progress

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// Progress reports phases to stderr (only when attached to a terminal).
type Progress struct {
	enabled bool
}

// New creates a progress reporter. Silent if stderr is not a terminal.
func New() *Progress {
	return &Progress{
		enabled: term.IsTerminal(int(os.Stderr.Fd())),
	}
}

// Phase prints a phase name to stderr, overwriting the previous line.
func (p *Progress) Phase(format string, args ...interface{}) {
	if !p.enabled {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "\r\033[K%s", msg)
}

// Done finishes progress output with a newline.
func (p *Progress) Done() {
	if !p.enabled {
		return
	}
	fmt.Fprintln(os.Stderr)
}

// Spinner shows an animated spinner with a label while a long-running operation executes.
//
// Usage:
//
//	sp := progress.NewSpinner("Classifying via Claude Code")
//	sp.Start()
//	result := doWork()
//	sp.Stop()
type Spinner struct {
	mu      sync.Mutex // protects label mutation from Update while goroutine reads
	label   string
	frames  []string
	enabled bool

	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	running atomic.Bool
}

// NewSpinner creates a spinner with the given label. The rendered form
// includes an elapsed-time counter in seconds so the user knows the spinner
// isn't frozen during slow LLM calls.
func NewSpinner(label string) *Spinner {
	return &Spinner{
		label:   label,
		frames:  []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		enabled: term.IsTerminal(int(os.Stderr.Fd())),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start begins spinning in a goroutine. Each render shows the current
// label plus elapsed seconds, so users can distinguish a stuck spinner
// from a slow LLM round trip.
func (s *Spinner) Start() {
	if !s.enabled {
		return
	}
	s.running.Store(true)
	start := time.Now()
	go func() {
		defer close(s.done)
		i := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.mu.Lock()
				label := s.label
				s.mu.Unlock()
				elapsed := int(time.Since(start).Seconds())
				fmt.Fprintf(os.Stderr, "\r\033[K%s %s \033[2m[%ds]\033[0m",
					s.frames[i%len(s.frames)], label, elapsed)
				i++
			}
		}
	}()
}

// Stop halts the spinner and clears the line.
func (s *Spinner) Stop() {
	if !s.enabled || !s.running.Load() {
		return
	}
	s.once.Do(func() { close(s.stop) })
	<-s.done
	fmt.Fprintf(os.Stderr, "\r\033[K")
	s.running.Store(false)
}

// Update changes the spinner label without stopping.
func (s *Spinner) Update(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}
