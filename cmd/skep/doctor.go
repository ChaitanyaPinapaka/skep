package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/registry"
	"github.com/ChaitanyaPinapaka/skep/internal/trust"
)

// cmdDoctor runs a dependency + environment health check. Surfaces anything
// missing or misconfigured, with concrete install commands. Exit code 0 if
// everything's healthy, 1 if any critical check fails.
func cmdDoctor(args []string) error {
	jsonOut := false
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		}
	}

	checks := []doctorCheck{
		checkSkepVersion(),
		checkOS(),
		checkClaudeCLI(),
		checkGit(),
		checkTmux(),
		checkCtags(),
		checkSkepDir(),
		checkWorkspace(),
	}

	if jsonOut {
		printDoctorJSON(checks)
	} else {
		printDoctorText(checks)
	}

	for _, c := range checks {
		if c.Level == doctorCritical && !c.OK {
			return fmt.Errorf("doctor found critical issues")
		}
	}
	return nil
}

type doctorLevel int

const (
	doctorInfo doctorLevel = iota
	doctorWarning
	doctorCritical
)

type doctorCheck struct {
	Name    string      `json:"name"`
	OK      bool        `json:"ok"`
	Level   doctorLevel `json:"level"`
	Message string      `json:"message"`
	FixHint string      `json:"fix_hint,omitempty"`
}

func checkSkepVersion() doctorCheck {
	return doctorCheck{
		Name:    "skep version",
		OK:      true,
		Level:   doctorInfo,
		Message: version,
	}
}

func checkOS() doctorCheck {
	supported := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	c := doctorCheck{
		Name:    "operating system",
		OK:      supported,
		Level:   doctorWarning,
		Message: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if !supported {
		c.Message = runtime.GOOS + " (untested — Linux/macOS/WSL2 only)"
		c.FixHint = "run skep on Linux, macOS, or WSL2"
	}
	return c
}

func checkClaudeCLI() doctorCheck {
	path, err := exec.LookPath("claude")
	if err != nil {
		return doctorCheck{
			Name:    "claude (Claude Code CLI)",
			OK:      false,
			Level:   doctorCritical,
			Message: "not found on PATH",
			FixHint: "install from https://docs.claude.com/en/docs/claude-code",
		}
	}
	// Grab version non-fatally
	out, _ := exec.Command("claude", "--version").CombinedOutput()
	v := strings.TrimSpace(string(out))
	if v == "" {
		v = path
	}
	return doctorCheck{
		Name:    "claude (Claude Code CLI)",
		OK:      true,
		Level:   doctorCritical,
		Message: v,
	}
}

func checkGit() doctorCheck {
	path, err := exec.LookPath("git")
	if err != nil {
		return doctorCheck{
			Name:    "git",
			OK:      false,
			Level:   doctorWarning,
			Message: "not found on PATH",
			FixHint: "install git for faster indexing (git ls-files); skep will fall back to filesystem walking without it",
		}
	}
	out, _ := exec.Command("git", "--version").Output()
	return doctorCheck{
		Name:    "git",
		OK:      true,
		Level:   doctorInfo,
		Message: strings.TrimSpace(string(out)) + " (" + path + ")",
	}
}

func checkTmux() doctorCheck {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return doctorCheck{
			Name:    "tmux",
			OK:      false,
			Level:   doctorCritical,
			Message: "not found on PATH",
			FixHint: installHint(
				"apt", "sudo apt install tmux",
				"brew", "brew install tmux",
				"dnf", "sudo dnf install tmux",
			),
		}
	}
	out, _ := exec.Command("tmux", "-V").Output()
	return doctorCheck{
		Name:    "tmux",
		OK:      true,
		Level:   doctorCritical,
		Message: strings.TrimSpace(string(out)) + " (" + path + ")",
	}
}

func checkCtags() doctorCheck {
	path, err := exec.LookPath("ctags")
	if err != nil {
		return doctorCheck{
			Name:  "universal-ctags",
			OK:    false,
			Level: doctorWarning,
			Message: "not found on PATH — files in languages without a native tree-sitter grammar " +
				"(Rust, Ruby, Elixir, Swift, C/C++, etc.) will be indexed with zero symbols",
			FixHint: installHint(
				"apt", "sudo apt install universal-ctags",
				"brew", "brew install universal-ctags",
				"dnf", "sudo dnf install ctags",
			),
		}
	}
	out, _ := exec.Command("ctags", "--version").Output()
	firstLine := strings.SplitN(string(out), "\n", 2)[0]
	return doctorCheck{
		Name:    "universal-ctags",
		OK:      true,
		Level:   doctorInfo,
		Message: firstLine + " (" + path + ")",
	}
}

func checkSkepDir() doctorCheck {
	root, err := repoRoot()
	if err != nil {
		return doctorCheck{
			Name:    ".skep dir",
			OK:      false,
			Level:   doctorInfo,
			Message: "not inside a git repo or a skep-initialized directory",
			FixHint: "run 'skep init' to initialize this repo",
		}
	}
	rdir := filepath.Join(root, ".skep")
	if _, err := os.Stat(rdir); err != nil {
		return doctorCheck{
			Name:    ".skep dir",
			OK:      false,
			Level:   doctorInfo,
			Message: "not initialized at " + rdir,
			FixHint: "run 'skep init' to initialize this repo",
		}
	}
	cfg := config.Load(rdir)
	testCmdTrusted := false
	if cfg.TestCmd != "" {
		ok, _ := trust.IsAllowed(repoRootFromSkepDir(rdir), cfg.TestCmd)
		testCmdTrusted = ok
	}
	return doctorCheck{
		Name:    ".skep dir",
		OK:      true,
		Level:   doctorInfo,
		Message: fmt.Sprintf("%s (llm=%s, test_cmd_trusted=%v)", rdir, cfg.LLM, testCmdTrusted),
	}
}

// repoRootFromSkepDir derives the repo path from a .skep directory
// path — just strips the trailing /.skep segment. Used by doctor so
// it can ask the trust store the same question the executor asks.
func repoRootFromSkepDir(skepDir string) string {
	return filepath.Dir(skepDir)
}

func checkWorkspace() doctorCheck {
	reg, err := registry.Load()
	if err != nil || reg == nil || reg.WorkspaceRoot() == "" {
		return doctorCheck{
			Name:    "workspace",
			OK:      false,
			Level:   doctorInfo,
			Message: "no workspace found",
			FixHint: "run 'skep init' to create or join a workspace",
		}
	}
	return doctorCheck{
		Name:    "workspace",
		OK:      true,
		Level:   doctorInfo,
		Message: fmt.Sprintf("%s (%d repos registered)", reg.WorkspaceRoot(), len(reg.Agents)),
	}
}

// installHint picks the best install command based on which package manager
// is available on the system. Order matters: first match wins.
func installHint(pairs ...string) string {
	if len(pairs)%2 != 0 {
		return ""
	}
	for i := 0; i < len(pairs); i += 2 {
		mgr := pairs[i]
		cmd := pairs[i+1]
		if _, err := exec.LookPath(mgr); err == nil {
			return cmd
		}
	}
	// Fallback: show all options
	var opts []string
	for i := 0; i < len(pairs); i += 2 {
		opts = append(opts, fmt.Sprintf("%s → %s", pairs[i], pairs[i+1]))
	}
	return strings.Join(opts, " | ")
}

func printDoctorText(checks []doctorCheck) {
	allCritical := true
	for _, c := range checks {
		var marker string
		switch {
		case c.OK:
			marker = "\x1b[32m✓\x1b[0m"
		case c.Level == doctorCritical:
			marker = "\x1b[31m✗\x1b[0m"
			allCritical = false
		default:
			marker = "\x1b[33m!\x1b[0m"
		}
		fmt.Printf("  %s %-28s %s\n", marker, c.Name, c.Message)
		if !c.OK && c.FixHint != "" {
			fmt.Printf("      \x1b[2mfix:\x1b[0m %s\n", c.FixHint)
		}
	}
	fmt.Println()
	if allCritical {
		fmt.Println("\x1b[32mskep is healthy.\x1b[0m")
	} else {
		fmt.Println("\x1b[31mskep has critical issues — see above.\x1b[0m")
	}
}

func printDoctorJSON(checks []doctorCheck) {
	type jsonCheck struct {
		Name    string `json:"name"`
		OK      bool   `json:"ok"`
		Level   string `json:"level"`
		Message string `json:"message"`
		FixHint string `json:"fix_hint,omitempty"`
	}
	levelName := func(l doctorLevel) string {
		switch l {
		case doctorCritical:
			return "critical"
		case doctorWarning:
			return "warning"
		}
		return "info"
	}
	var out []jsonCheck
	for _, c := range checks {
		out = append(out, jsonCheck{
			Name: c.Name, OK: c.OK, Level: levelName(c.Level),
			Message: c.Message, FixHint: c.FixHint,
		})
	}
	jsonOrText(nil, out, func() {})
	// jsonOrText needs a --json flag — just marshal directly
	for _, c := range out {
		fmt.Printf(`{"name":%q,"ok":%v,"level":%q,"message":%q,"fix_hint":%q}`+"\n",
			c.Name, c.OK, c.Level, c.Message, c.FixHint)
	}
}
