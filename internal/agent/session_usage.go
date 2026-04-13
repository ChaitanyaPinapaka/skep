package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// SessionUsage summarizes what a Claude Code session cost. Populated by
// reading the session JSONL file that Claude Code writes to
// ~/.claude/projects/<encoded-path>/<session-uuid>.jsonl after a run.
type SessionUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	// EffectiveBilledInput approximates Anthropic's billing with the
	// 90% cache-read discount: input + cache_read × 0.1.
	EffectiveBilledInput int            `json:"effective_billed_input"`
	Turns                int            `json:"turns"`
	ToolCalls            int            `json:"tool_calls"`
	ToolCallsByName      map[string]int `json:"tool_calls_by_name"`
}

// ReadSessionUsage locates the Claude Code session file for a given repo
// root + session UUID and returns aggregate usage. Returns nil (no error)
// if the session file cannot be found or parsed — this is best-effort
// observability, never load-bearing for task correctness.
func ReadSessionUsage(repoRoot, sessionID string) *SessionUsage {
	if sessionID == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	// Claude Code encodes the working directory as a dir name by replacing
	// slashes with dashes. The absolute path `/mnt/c/Work/skep` becomes
	// `-mnt-c-Work-skep`.
	abs, _ := filepath.Abs(repoRoot)
	encoded := strings.ReplaceAll(abs, string(filepath.Separator), "-")
	sessionPath := filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")

	f, err := os.Open(sessionPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	u := &SessionUsage{ToolCallsByName: map[string]int{}}

	type block struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	}
	type msg struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
		Usage   struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	type entry struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Type != "assistant" || len(e.Message) == 0 {
			continue
		}
		var m msg
		if err := json.Unmarshal(e.Message, &m); err != nil {
			continue
		}
		if m.Role != "assistant" {
			continue
		}
		u.Turns++
		u.InputTokens += m.Usage.InputTokens
		u.OutputTokens += m.Usage.OutputTokens
		u.CacheCreationInputTokens += m.Usage.CacheCreationInputTokens
		u.CacheReadInputTokens += m.Usage.CacheReadInputTokens
		for _, c := range m.Content {
			if c.Type == "tool_use" {
				u.ToolCalls++
				if c.Name != "" {
					u.ToolCallsByName[c.Name]++
				}
			}
		}
	}
	u.EffectiveBilledInput = u.InputTokens + u.CacheReadInputTokens/10
	return u
}
