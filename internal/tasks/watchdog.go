package tasks

import (
	"regexp"
	"strings"
	"sync"
)

// DefaultApprovalPatterns is the built-in regex list the approval
// watchdog uses when the user has not configured their own via
// .skep/config.json → approval_patterns.
//
// Patterns are matched against the tail of each executing task's tmux
// pane. Every pattern must be a valid Go RE2 regex. Case-insensitive
// matching is applied automatically via the (?i) prefix.
//
// Keep this list conservative — false positives rename the tmux
// window to "[!]" and surface a bell in the status line, which is
// annoying if it fires on prose that happens to contain the word
// "proceed". The patterns below look for interactive prompt shapes
// (question mark at end of line, [y/n] / (Y/n) markers) rather than
// bare keywords.
var DefaultApprovalPatterns = []string{
	`(?i)do you want to proceed\??`,
	`(?i)continue\??\s*\[y/n\]`,
	`(?i)continue\??\s*\(y/n\)`,
	`(?i)apply these edits\??`,
	`(?i)confirm\??\s*\[y/n\]`,
	`(?i)press\s+(enter|return)\s+to\s+continue`,
	`(?i)\(y/n\)\s*:?\s*$`,
	`(?i)\[y/n\]\s*:?\s*$`,
}

// compiledApprovalCache memoizes compiled patterns keyed by the
// joined pattern list. Patterns rarely change but the watchdog runs
// frequently, so this keeps the hot path allocation-free.
var compiledApprovalCache sync.Map // key=strings.Join(..., "\n") → []*regexp.Regexp

// ApprovalPatterns returns the compiled regex list for the watchdog.
// When userPatterns is empty, the built-in defaults are used. Invalid
// regexes are skipped silently so one bad user entry does not
// disable the whole watchdog — callers that care about reporting
// bad patterns should use ValidatePatterns at startup.
func ApprovalPatterns(userPatterns []string) []*regexp.Regexp {
	patterns := userPatterns
	if len(patterns) == 0 {
		patterns = DefaultApprovalPatterns
	}
	key := strings.Join(patterns, "\n")
	if cached, ok := compiledApprovalCache.Load(key); ok {
		return cached.([]*regexp.Regexp)
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}
	compiledApprovalCache.Store(key, compiled)
	return compiled
}

// ValidatePatterns returns the list of user-provided patterns that
// failed to compile. Intended for daemon startup logging so bad
// config entries surface at boot instead of silently disappearing.
func ValidatePatterns(userPatterns []string) []string {
	var bad []string
	for _, p := range userPatterns {
		if _, err := regexp.Compile(p); err != nil {
			bad = append(bad, p)
		}
	}
	return bad
}

// DetectApprovalPrompt runs the compiled patterns against captured
// pane text and returns true on any match. The input is expected to
// be the last 20–30 lines of a pane; the full scrollback would waste
// CPU and produce false positives on historical prose.
func DetectApprovalPrompt(paneText string, patterns []*regexp.Regexp) bool {
	if paneText == "" || len(patterns) == 0 {
		return false
	}
	for _, re := range patterns {
		if re.MatchString(paneText) {
			return true
		}
	}
	return false
}
