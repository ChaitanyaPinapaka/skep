package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/llm"
)

// safeIdent matches model names, preset names — alphanumeric, dot, dash, underscore.
// Used to validate config values that could otherwise be shell-injected (defense in depth).
var safeIdent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// validateConfig sanitizes config values loaded from .skep/config.json.
//
// Config is untrusted: a cloned repo's config.json can contain anything
// a teammate committed. Any value that flows into a shell-out or a
// runtime decision gets validated here. Unknown or unsafe values fall
// back to the default — never to a shell-injected template.
//
// v0.1.0 is Claude-only. A config that names a preset we don't ship
// (e.g. `gemini`, `codex` from a config file written by v0.2.0 or by
// hand) gets coerced back to `claude` so the tool stays predictable
// instead of failing at the first shell-out with a confusing error.
func validateConfig(c *Config) {
	if c.Model != "" && !safeIdent.MatchString(c.Model) {
		c.Model = "" // reject — fall back to default
	}
	if c.ModelClassify != "" && !safeIdent.MatchString(c.ModelClassify) {
		c.ModelClassify = ""
	}
	// LLM must be a registered preset. Raw command templates are
	// intentionally no longer accepted in v0.1.0 — users who need a
	// custom wrapper can add it in v0.2.0 once the full preset surface
	// is re-enabled. Until then, anything off the allow-list becomes
	// `claude`.
	if c.LLM == "" || !isSupportedPreset(c.LLM) {
		c.LLM = llm.DefaultPreset
	}
	if c.LLMClassify != "" && !isSupportedPreset(c.LLMClassify) {
		c.LLMClassify = ""
	}
	if c.LLMDedup != "" && !isSupportedPreset(c.LLMDedup) {
		c.LLMDedup = ""
	}
	if c.ModelDedup != "" && !safeIdent.MatchString(c.ModelDedup) {
		c.ModelDedup = ""
	}
	// Validate tmux layout — only allow known values
	switch c.TmuxLayout {
	case "split-h", "split-v", "window", "popup":
		// ok
	default:
		c.TmuxLayout = "split-h"
	}
}

// isSupportedPreset reports whether a preset name is registered.
// Exists so validateConfig can reject unknown presets without pulling
// in regex validation — the preset map is the authoritative allow-list
// for v0.1.0 (Claude only) and v0.2.0 (Claude + Gemini + Codex).
func isSupportedPreset(name string) bool {
	_, ok := llm.Presets[name]
	return ok
}

// Config holds per-repo configuration stored in .skep/config.json.
type Config struct {
	LLM              string `json:"llm"`                          // preset name for execution (default: claude)
	LLMClassify      string `json:"llm_classify,omitempty"`       // preset name for classify+plan (default: same as llm)
	LLMDedup         string `json:"llm_dedup,omitempty"`          // preset name for LLM semantic dedup (default: same as llm_classify)
	Model            string `json:"model,omitempty"`              // model for execution (e.g., sonnet, opus)
	ModelClassify    string `json:"model_classify,omitempty"`     // model for classification (e.g., opus)
	ModelDedup       string `json:"model_dedup,omitempty"`        // model for LLM dedup (default: haiku — cheap, fast, good enough)
	TestCmd          string `json:"test_cmd,omitempty"`           // command to run tests after task execution
	AutoExecuteSmall bool   `json:"auto_execute_small,omitempty"` // auto-approve small tasks
	// Deprecated: trust for running test_cmd now lives in
	// ~/.skep/trusted-repos.json (see internal/trust). The field stays
	// on the struct so old config.json files still unmarshal cleanly,
	// but its value is IGNORED at runtime — the out-of-repo store is
	// the only source of truth. Safe to delete in v0.2.0.
	Trusted    bool   `json:"trusted,omitempty"`
	TmuxLayout string `json:"tmux_layout,omitempty"` // tmux spawn mode: split-h, split-v, window, popup (default: split-h)

	// Approval watchdog — detects when the executing LLM pauses on a
	// confirmation prompt ("Do you want to proceed?", "Continue? [y/n]",
	// …) in a background tmux pane so the daemon can surface it via
	// [!] window name prefix + `skep status --oneline`.
	//
	// ApprovalWatchdog toggles the feature; true is the default for new
	// installs. ApprovalPatterns is a list of Go regexps matched against
	// the last ~30 lines of each executing task's pane. Empty means
	// "use the built-in defaults" (see tasks.DefaultApprovalPatterns).
	ApprovalWatchdog *bool    `json:"approval_watchdog,omitempty"`
	ApprovalPatterns []string `json:"approval_patterns,omitempty"`

	// StepModelByVerb optionally routes step execution to different
	// models based on plan verb. Example:
	//   {"modify": "haiku", "test": "haiku", "add": "opus"}
	// Verbs not listed fall through to the task's main LLM command.
	// Empty map (the default) routes everything to the main model.
	// Policy is deferred — the infrastructure is here so future releases
	// can populate the table without a schema migration.
	StepModelByVerb map[string]string `json:"step_model_by_verb,omitempty"`
}

// ApprovalWatchdogEnabled returns whether the watchdog should run.
// Default is true — set ApprovalWatchdog=false to disable explicitly.
func (c *Config) ApprovalWatchdogEnabled() bool {
	if c.ApprovalWatchdog == nil {
		return true
	}
	return *c.ApprovalWatchdog
}

// Defaults returns a config with default values.
func Defaults() *Config {
	return &Config{
		LLM:        llm.DefaultPreset,
		TmuxLayout: "split-h",
	}
}

// Load reads config from .skep/config.json, returning defaults if not found.
// Validates all values to prevent injection from a maliciously crafted config.json.
//
// Parse errors are surfaced to stderr once at load time. Previously
// they were silently swallowed, which meant a user with a malformed
// config.json would see the tool use default values and have no idea
// why their settings weren't taking effect. Now they get a clear
// message naming the file and the parse error, while still falling
// back to defaults so the tool stays usable.
func Load(skepDir string) *Config {
	cfg := Defaults()
	path := filepath.Join(skepDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "skep: %s is malformed (%v) — using defaults. Fix the file or re-run 'skep init'.\n", path, err)
		return Defaults()
	}
	validateConfig(cfg)
	return cfg
}

// Save writes config to .skep/config.json.
func Save(skepDir string, cfg *Config) error {
	if err := os.MkdirAll(skepDir, 0o755); err != nil {
		return fmt.Errorf("create skep dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(filepath.Join(skepDir, "config.json"), data, 0o644)
}

// LLMCmd returns the resolved LLM execution command template (interactive).
func (c *Config) LLMCmd() string {
	cmd := llm.Resolve(c.LLM)
	return c.injectModel(cmd, c.Model)
}

// DaemonCmd returns the resolved LLM command for daemon execution (non-interactive, no permissions).
func (c *Config) DaemonCmd() string {
	cmd := llm.ResolveDaemon(c.LLM)
	return c.injectModel(cmd, c.Model)
}

// ClassifyCmd returns the resolved LLM classification command template
// for the parallel pipeline's classifier half. Cheap and fast: the
// classifier only decides size + confidence + clarify/reject, so we
// default to Haiku when the backend is Claude. Override via
// `model_classify` in config.json.
func (c *Config) ClassifyCmd() string {
	preset := c.LLM
	if c.LLMClassify != "" {
		preset = c.LLMClassify
	}
	cmd := llm.ResolveClassify(preset)
	model := c.ModelClassify
	if model == "" && strings.HasPrefix(cmd, "claude") {
		// Default classifier model: Haiku 4.5 for Claude backends.
		// Falls through to whatever the preset picks for others.
		model = "claude-haiku-4-5-20251001"
	}
	return c.injectModel(cmd, model)
}

// PlanCmd returns the resolved LLM command for the parallel pipeline's
// plan-generation half. Always uses the user's top model — plan quality
// matters more than plan cost, and the MCP-enabled call consumes the
// bulk of the wall time anyway.
func (c *Config) PlanCmd() string {
	preset := c.LLM
	if c.LLMClassify != "" {
		// Reuse the classify preset for the LLM binary/flags, but not
		// for the model — plan needs the heavy-weight reasoning model.
		preset = c.LLMClassify
	}
	cmd := llm.ResolveClassify(preset)
	// Prefer the user-configured execution model (Opus/highest) over
	// the classifier model. Only fall back to ModelClassify if Model
	// is unset — unusual but preserves existing single-model configs.
	model := c.Model
	if model == "" {
		model = c.ModelClassify
	}
	return c.injectModel(cmd, model)
}

// DedupCmd returns the resolved LLM command template for semantic dedup.
// Defaults to Haiku (cheap, fast, good enough for a "are these two
// sentences about the same thing?" call) — Opus-level reasoning is
// overkill and burns money on every task create. Falls back to the
// classify command if the user hasn't configured a dedup backend.
func (c *Config) DedupCmd() string {
	preset := c.LLMDedup
	if preset == "" {
		preset = c.LLMClassify
	}
	if preset == "" {
		preset = c.LLM
	}
	cmd := llm.ResolveClassify(preset)
	model := c.ModelDedup
	if model == "" {
		// Default to Haiku 4.5 for Claude. For other providers we fall
		// through to their smallest/cheapest model via injectModel's
		// no-op on empty, so the user gets whatever the preset picks.
		if strings.HasPrefix(cmd, "claude") {
			model = "claude-haiku-4-5-20251001"
		}
	}
	return c.injectModel(cmd, model)
}

// injectModel inserts the model-selection flag right after the
// binary name in a command template, without touching the rest of
// the argv. It operates on the tokenized form so a template like
// `claude-wrapper -p "{prompt}"` doesn't get its prefix mauled by a
// naive string replace — the old substring-replace implementation
// would have turned `claude-wrapper` into `claude --model X-wrapper`,
// invoking the wrong binary.
//
// Supported backends (v0.1.0): claude. v0.2.0 will re-add gemini.
// For any unknown binary the model is appended as `--model <val>`
// after the binary — if the tool doesn't understand it, we surface a
// clean error at exec time instead of silently mangling the command.
func (c *Config) injectModel(cmd, model string) string {
	if model == "" {
		return cmd
	}
	argv, err := llm.ShlexSplit(cmd)
	if err != nil || len(argv) == 0 {
		return cmd
	}
	// Only inject when the binary name (not the command template) matches.
	// Use base name so `/usr/local/bin/claude` still gets recognized.
	base := argv[0]
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}

	var flag, value string
	switch base {
	case "claude":
		flag, value = "--model", model
	case "gemini":
		flag, value = "-m", model
	default:
		// Unknown binary: append --model as a generic flag.
		flag, value = "--model", model
	}
	// Insert flag + value right after argv[0]. Re-join using the same
	// quoting rules as the original template (re-quote any arg that
	// contains whitespace) so placeholders like "{prompt}" stay intact.
	injected := append([]string{argv[0], flag, value}, argv[1:]...)
	return joinArgv(injected)
}

// joinArgv produces a shell-safe command string from a tokenized
// argv. Quotes any arg containing whitespace or a double quote. Not
// a full shell quoter — it's only used to re-serialize templates
// after injectModel adds a flag, and the inputs are known to be well
// formed because they came from ShlexSplit.
func joinArgv(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if strings.ContainsAny(a, " \t") {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(a, `"`, `\"`))
			b.WriteByte('"')
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}
