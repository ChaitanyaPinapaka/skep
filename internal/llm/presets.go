package llm

// Preset LLM CLI configurations. Each preset has three templates:
//
//	CmdTemplate      — interactive execution (skep run, TUI mode)
//	ClassifyTemplate — non-interactive classification (skep task, reads stdout)
//	DaemonTemplate   — non-interactive execution (daemon mode, no permission prompts)
//
// v0.1.0 ships Claude Code only. Gemini CLI and Codex CLI presets are
// deliberately NOT registered here because they have not been tested
// end-to-end with the parallel classify+plan pipeline, the approval
// watchdog, or cross-repo delegation. The plumbing in shellout.go and
// config.go is generic — v0.2.0 re-adds those presets after the test
// matrix is green.
var Presets = map[string]Preset{
	"claude": {
		Name:        "claude",
		DisplayName: "Claude Code",
		// Interactive: system prompt, user prompt. MCP injected at runtime.
		CmdTemplate: `claude --append-system-prompt "{system_prompt}" "{prompt}"`,
		// Classify: -p (print mode), no tools, no system prompt envelope
		ClassifyTemplate: `claude -p "{prompt}"`,
		// Daemon: -p, skip permissions, allow edit/write/bash
		DaemonTemplate: `claude -p --dangerously-skip-permissions --allowedTools "Edit,Write,Bash" "{prompt}"`,
	},
}

const DefaultPreset = "claude"

// Preset defines a known LLM CLI configuration.
type Preset struct {
	Name             string
	DisplayName      string
	CmdTemplate      string // interactive execution (skep run)
	ClassifyTemplate string // non-interactive classification (skep task)
	DaemonTemplate   string // non-interactive execution (daemon mode, no permission prompts)
}

// SupportsMCP returns true if the CLI supports --mcp-config.
// Currently only Claude Code has first-class MCP client support,
// which is part of why v0.1.0 is Claude-only.
func (p Preset) SupportsMCP() bool {
	return p.Name == "claude"
}

// Resolve takes a config value — either a preset name or a raw command template —
// and returns the execution command template.
func Resolve(value string) string {
	if p, ok := Presets[value]; ok {
		return p.CmdTemplate
	}
	return value
}

// ResolveClassify returns the classification command template.
func ResolveClassify(value string) string {
	if p, ok := Presets[value]; ok {
		return p.ClassifyTemplate
	}
	return value
}

// ResolveDaemon returns the daemon (non-interactive, no permissions) command template.
func ResolveDaemon(value string) string {
	if p, ok := Presets[value]; ok {
		return p.DaemonTemplate
	}
	return value
}

// PresetName returns the preset name if the command matches a known preset, or "" otherwise.
func PresetName(cmdTemplate string) string {
	for name, p := range Presets {
		if p.CmdTemplate == cmdTemplate {
			return name
		}
	}
	return ""
}
