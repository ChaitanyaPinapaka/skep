package llm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ShellOut executes the LLM CLI interactively (full TUI).
func ShellOut(repoRoot, cmdTemplate, prompt string) (string, error) {
	return runLLM(context.Background(), repoRoot, cmdTemplate, prompt, "", "", true)
}

// ShellOutWithSystem executes the LLM CLI interactively with a system prompt.
func ShellOutWithSystem(repoRoot, cmdTemplate, prompt, systemPrompt string) (string, error) {
	return runLLM(context.Background(), repoRoot, cmdTemplate, prompt, systemPrompt, "", true)
}

// ShellOutQuiet runs the LLM CLI non-interactively — captures all output.
// Use ShellOutQuietCtx if you have a context that should cancel the child.
func ShellOutQuiet(repoRoot, cmdTemplate, prompt string) (string, error) {
	return runLLM(context.Background(), repoRoot, cmdTemplate, prompt, "", "", false)
}

// ShellOutQuietCtx runs the LLM CLI non-interactively with a cancellable context.
// When ctx is cancelled, the child process is killed. Use this in daemon loops
// so `skep daemon stop` can interrupt a running classifier call.
func ShellOutQuietCtx(ctx context.Context, repoRoot, cmdTemplate, prompt string) (string, error) {
	return runLLM(ctx, repoRoot, cmdTemplate, prompt, "", "", false)
}

// ShellOutQuietWithMCP runs the LLM CLI non-interactively with the Skep MCP
// server registered so the model can call search_symbols, get_file_context,
// get_call_graph, etc. during classification.
//
// This is the "classify with tool use" path — slower per call than the
// tool-less ShellOutQuietCtx (the model spends tokens on tool calls instead
// of a single reasoning pass) but produces more grounded classifications
// because the model can dynamically explore the index instead of relying
// only on the pre-built context slice.
//
// Only Claude supports MCP today; on Gemini/Codex this behaves identically
// to ShellOutQuietCtx (mcpConfigFile is only injected for the `claude`
// binary in runLLM).
func ShellOutQuietWithMCP(ctx context.Context, repoRoot, cmdTemplate, prompt string) (string, error) {
	mcpConfigFile, err := writeMCPConfig(repoRoot)
	if err == nil {
		defer os.Remove(mcpConfigFile)
	}
	return runLLM(ctx, repoRoot, cmdTemplate, prompt, "", mcpConfigFile, false)
}

// ShellOutInteractive launches the LLM CLI with full TUI and MCP server attached.
// Returns (session_id, error).
func ShellOutInteractive(repoRoot, cmdTemplate, prompt, systemPrompt string) (string, error) {
	mcpConfigFile, err := writeMCPConfig(repoRoot)
	if err == nil {
		defer os.Remove(mcpConfigFile)
	}

	if _, err := runLLM(context.Background(), repoRoot, cmdTemplate, prompt, systemPrompt, mcpConfigFile, true); err != nil {
		return "", err
	}
	return findLatestSessionID(repoRoot), nil
}

// ShellOutResume resumes a session by ID with MCP attached.
func ShellOutResume(repoRoot, sessionID string) (string, error) {
	args := []string{"--resume", sessionID}

	mcpConfigFile, err := writeMCPConfig(repoRoot)
	if err == nil {
		defer os.Remove(mcpConfigFile)
		args = append([]string{"--mcp-config", mcpConfigFile}, args...)
	}

	c := exec.Command("claude", args...)
	c.Dir = repoRoot
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	if err := c.Run(); err != nil {
		return sessionID, err
	}
	return sessionID, nil
}

// runLLM is the core LLM execution function. It tokenizes the command template
// into argv (NO shell), substitutes placeholders with literal file contents,
// and execs directly. This eliminates ALL shell injection risk.
func runLLM(ctx context.Context, repoRoot, cmdTemplate, prompt, systemPrompt, mcpConfigFile string, interactive bool) (string, error) {
	// Tokenize the command template into argv. shlex-style parsing handles
	// quoted strings but does NOT execute shell commands.
	argv, err := shlexSplit(cmdTemplate)
	if err != nil {
		return "", fmt.Errorf("parse command template: %w", err)
	}
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command template")
	}

	// Substitute placeholders with literal values (not shell expansions)
	for i, arg := range argv {
		if arg == "{prompt}" {
			argv[i] = prompt
		} else if arg == "{system_prompt}" {
			argv[i] = systemPrompt
		}
	}

	// Remove --append-system-prompt and its value if systemPrompt is empty
	if systemPrompt == "" {
		argv = removeFlag(argv, "--append-system-prompt")
	}

	// Inject --mcp-config for Claude
	if mcpConfigFile != "" && filepath.Base(argv[0]) == "claude" {
		argv = append([]string{argv[0], "--mcp-config", mcpConfigFile}, argv[1:]...)
	}

	// exec.CommandContext kills the child process when ctx is cancelled.
	// For interactive runs we still want the user's Ctrl+C to propagate
	// via the inherited TTY, which CommandContext doesn't interfere with.
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = repoRoot

	if interactive {
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("llm command failed: %w", err)
		}
		return "completed", nil
	}

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return stdout.String() + stderr.String(), fmt.Errorf("llm command failed: %w", err)
	}
	return stdout.String(), nil
}

// removeFlag removes --flag and its following value from argv.
func removeFlag(argv []string, flag string) []string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return append(argv[:i], argv[i+2:]...)
		}
	}
	return argv
}

// ShlexSplit tokenizes a command string into argv, handling quoted strings.
// Does NOT execute shell commands or interpret metacharacters.
//
// Exported so other packages (e.g. internal/agent for test_cmd execution)
// can reuse the same safe tokenizer instead of reaching for os/exec
// with `bash -c`, which is the main command-injection vector in tools
// that run user-configured shell strings.
func ShlexSplit(s string) ([]string, error) {
	return shlexSplit(s)
}

// shlexSplit is the unexported implementation. Kept lowercase for
// internal callers in this package; external callers use ShlexSplit.
func shlexSplit(s string) ([]string, error) {
	var argv []string
	var current []byte
	var inQuote byte // 0 if not in quote, otherwise the quote char

	flush := func() {
		if len(current) > 0 || inQuote != 0 {
			argv = append(argv, string(current))
			current = nil
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
				continue
			}
			// Allow escape sequences inside quotes
			if c == '\\' && i+1 < len(s) {
				i++
				current = append(current, s[i])
				continue
			}
			current = append(current, c)
			continue
		}
		switch c {
		case ' ', '\t', '\n':
			flush()
		case '"', '\'':
			inQuote = c
		case '\\':
			if i+1 < len(s) {
				i++
				current = append(current, s[i])
			}
		default:
			current = append(current, c)
		}
	}
	if inQuote != 0 {
		return nil, fmt.Errorf("unclosed quote in command template")
	}
	flush()
	return argv, nil
}

// writeMCPConfig creates a temp JSON file with MCP server config.
func writeMCPConfig(repoRoot string) (string, error) {
	skepBin, err := os.Executable()
	if err != nil {
		skepBin = "skep"
	}

	// Use json.Marshal to escape paths properly (handles spaces, quotes)
	mcpConfig := fmt.Sprintf(`{
  "mcpServers": {
    "skep": {
      "command": %s,
      "args": ["mcp", "--repo", %s]
    }
  }
}`, jsonString(skepBin), jsonString(repoRoot))

	return writeTempFile("skep-mcp-*.json", mcpConfig)
}

// jsonString quotes a string for JSON.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writeTempFile writes content to a temp file with 0600 perms.
func writeTempFile(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	// CreateTemp uses 0600 by default, but be explicit
	os.Chmod(path, 0o600)
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("write temp file: %w", err)
	}
	f.Close()
	return path, nil
}

// findLatestSessionID finds the most recent session ID for a repo.
// Claude Code stores sessions at ~/.claude/projects/<encoded-path>/<session-uuid>.jsonl
// The encoded path is the absolute repo path with "/" replaced by "-".
func findLatestSessionID(repoRoot string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// Encode repo path: /home/user/code/backend → -home-user-code-backend
	encoded := strings.ReplaceAll(repoRoot, "/", "-")
	projectDir := filepath.Join(home, ".claude", "projects", encoded)

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ""
	}

	// Find the most recent .jsonl file
	var latestName string
	var latestTime int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Unix() > latestTime {
			latestTime = info.ModTime().Unix()
			latestName = strings.TrimSuffix(name, ".jsonl")
		}
	}
	return latestName
}

// PromptFilePath returns the standard prompt file path in a skep dir.
func PromptFilePath(skepDir string) string {
	return filepath.Join(skepDir, "current-prompt.md")
}
