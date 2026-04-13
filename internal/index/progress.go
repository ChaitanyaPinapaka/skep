package index

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// Progress reports indexing phases to stderr (only when attached to a terminal).
type Progress struct {
	enabled bool
}

// NewProgress creates a progress reporter. Silent if stderr is not a terminal.
func NewProgress() *Progress {
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
	fmt.Fprintf(os.Stderr, "\r\033[K%s", msg) // clear line, print
}

// Done finishes progress output with a newline.
func (p *Progress) Done() {
	if !p.enabled {
		return
	}
	fmt.Fprintln(os.Stderr)
}
