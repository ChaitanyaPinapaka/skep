package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/registry"
	"github.com/ChaitanyaPinapaka/skep/internal/trust"
)

func cmdInit(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	rdir := filepath.Join(root, ".skep")

	fmt.Printf("Initializing skep in %s\n\n", root)

	reader := bufio.NewReader(os.Stdin)

	// --- Step 1: Workspace ---
	wsRoot := registry.FindWorkspace(root)
	if wsRoot == "" {
		wsRoot = promptWorkspace(root, reader)
	} else {
		fmt.Printf("Workspace: %s\n", wsRoot)
	}

	// --- Step 2: LLM CLI ---
	// v0.1.0 supports Claude Code only. Gemini / Codex / custom backends
	// will return in v0.2.0 once they're actually tested end-to-end with
	// the parallel classify+plan pipeline and the cockpit integration.
	cfg := config.Defaults()
	cfg.LLM = "claude"
	fmt.Printf("\nLLM CLI: Claude Code (claude)\n")
	var input string

	// --- Step 3: Model ---
	// v0.1.0 is Claude-only, so the preset check collapses to `== "claude"`.
	// Retained as an explicit branch so v0.2.0 adding gemini/codex is a
	// one-line change rather than rewriting control flow.
	if cfg.LLM == "claude" {
		models := modelOptions(cfg.LLM)
		if len(models) > 0 {
			fmt.Printf("\nModel for execution:\n")
			for i, m := range models {
				fmt.Printf("  %d. %s\n", i+1, m)
			}
			fmt.Printf("Choice [1]: ")
			input = readLine(reader)
			idx := 0
			if input != "" && input != "1" {
				for i, m := range models {
					if input == fmt.Sprintf("%d", i+1) || input == m {
						idx = i
						break
					}
				}
			}
			cfg.Model = models[idx]
		}

		// --- Step 4: Different model for planning? ---
		fmt.Printf("\nUse a different model for classification/planning? [y/N]: ")
		input = readLine(reader)
		if input == "y" || input == "Y" {
			fmt.Printf("Model for classify/plan:\n")
			for i, m := range models {
				fmt.Printf("  %d. %s\n", i+1, m)
			}
			fmt.Printf("Choice: ")
			input = readLine(reader)
			for i, m := range models {
				if input == fmt.Sprintf("%d", i+1) || input == m {
					cfg.ModelClassify = models[i]
					break
				}
			}
		}
	}

	// --- Step 5: Test command ---
	detected := detectTestCmd(root)
	if detected != "" {
		fmt.Printf("\nDetected test command: %s\n", detected)
		fmt.Printf("Use this? [Y/n]: ")
		input = readLine(reader)
		if input == "" || input == "y" || input == "Y" {
			cfg.TestCmd = detected
		} else {
			fmt.Printf("Test command (or empty to skip): ")
			cfg.TestCmd = readLine(reader)
		}
	} else {
		fmt.Printf("\nTest command (e.g., 'go test ./...', 'npm test', or empty to skip): ")
		cfg.TestCmd = readLine(reader)
	}

	// --- Step 6: Auto-execute ---
	fmt.Printf("\nAuto-execute small tasks? [y/N]: ")
	input = readLine(reader)
	cfg.AutoExecuteSmall = input == "y" || input == "Y"

	// --- Record test_cmd trust in the out-of-repo store ---
	// Trust lives in ~/.skep/trusted-repos.json keyed by absolute
	// repo path, NOT in .skep/config.json. This prevents a malicious
	// or sloppy config.json committed upstream from silently enabling
	// test_cmd execution on a collaborator's machine after `git pull`.
	// If the user edits test_cmd later, IsAllowed will return false
	// until they re-run `skep init` to acknowledge the new value.
	if cfg.TestCmd != "" {
		if err := trust.MarkTrusted(root, cfg.TestCmd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not mark repo trusted: %v\n", err)
		}
	}

	// --- Write config ---
	if err := os.MkdirAll(rdir, 0o755); err != nil {
		return err
	}
	if err := config.Save(rdir, cfg); err != nil {
		return err
	}

	// --- Add .skep/ to .gitignore ---
	addToGitignore(root)

	// --- Optional: warn if ctags is not installed ---
	// Tree-sitter handles Go/TS/JS/Python/Kotlin/Java natively. For ~90 other
	// languages (Rust, Ruby, Elixir, Lua, Zig, Scala, Swift, C/C++, etc.) we
	// fall back to universal-ctags. Without it those files are detected but
	// not symbol-parsed.
	if _, err := exec.LookPath("ctags"); err != nil {
		fmt.Println()
		fmt.Println("Note: universal-ctags is not installed.")
		fmt.Println("  Without it, files in Rust/Ruby/Elixir/Swift/C/C++/etc. will")
		fmt.Println("  be indexed with zero symbols. Install for better coverage:")
		fmt.Println("    Ubuntu/Debian:  sudo apt install universal-ctags")
		fmt.Println("    macOS:          brew install universal-ctags")
	}

	// --- Index ---
	fmt.Println()
	if err := cmdIndex(args); err != nil {
		return err
	}

	// --- Step 8: Start the daemon (optional, inside tmux) ---
	fmt.Printf("\nStart the agent daemon now? [Y/n]: ")
	input = readLine(reader)
	if input == "" || input == "y" || input == "Y" {
		if err := startDaemonInTmux(root); err != nil {
			fmt.Fprintf(os.Stderr, "could not start daemon: %v\n", err)
			fmt.Fprintln(os.Stderr, "you can start it manually later: tmux new-session -d -s skep-<reponame> 'skep daemon'")
		}
	}

	// --- Step 9: Write the cockpit tmux config unconditionally ---
	// Idempotent; backs up any existing skep.conf automatically. No
	// prompt because v0.1.0 gates on tmux via `skep doctor`, so every
	// user wants the cockpit wired on first run.
	if err := cmdCockpitSetup(nil); err != nil {
		fmt.Fprintf(os.Stderr, "\ncould not write cockpit config: %v\n", err)
		fmt.Fprintln(os.Stderr, "run 'skep cockpit setup' manually later")
	}

	// --- Recommend cockpit layout ---
	printCockpitInstructions(root)

	return nil
}

// printCockpitInstructions prints the recommended cockpit flow:
// open one tmux session, drive skep from the main pane, let approved
// tasks land as new windows on the right, rely on the tmux status line
// (fed by 'skep status --oneline') for ambient workspace awareness.
// The full apiary is one keystroke away via 'Ctrl+b S' once the
// cockpit tmux config has been sourced.
func printCockpitInstructions(root string) {
	fmt.Println()
	fmt.Println("── ◠  Cockpit ──")
	fmt.Println()
	fmt.Println("One tmux session for the whole workspace. The cockpit tmux config")
	fmt.Println("(written by 'skep cockpit setup') adds keybinds, mouse scroll, big")
	fmt.Println("scrollback, and a status line fed by 'skep status --oneline'. No")
	fmt.Println("permanent dashboard pane needed.")
	fmt.Println()
	fmt.Println("First run:")
	fmt.Println("    skep cockpit setup")
	fmt.Println("    echo 'source-file ~/.tmux.conf.d/skep.conf' >> ~/.tmux.conf")
	fmt.Println("    tmux new -s work")
	fmt.Println("    # inside tmux: Ctrl+b : source-file ~/.tmux.conf")
	fmt.Println()
	fmt.Println("Daily use:")
	fmt.Println("    tmux attach -t work")
	fmt.Println("    skep task create \"your first task\"")
	fmt.Println()
	fmt.Println("Inside the cockpit:")
	fmt.Println("    Ctrl+b S   Pop up the full apiary (workspace watch)")
	fmt.Println("    Ctrl+b T   Pop up skep tasks")
	fmt.Println("    Ctrl+b !   Jump to oldest task waiting on approval")
	fmt.Println("    Ctrl+b 1-9 Jump to task window by number")
	fmt.Println()
	fmt.Println("Peek at the background daemon any time with:")
	fmt.Printf("    tmux attach -t skep-%s    (Ctrl+B d to detach)\n", filepath.Base(root))
	fmt.Println()
}

// startDaemonInTmux starts the skep daemon in a detached tmux session.
// The daemon is hidden by default — task panes spawn into whatever session
// the user is currently attached to, so the daemon stays out of sight unless
// the user explicitly attaches for debugging.
func startDaemonInTmux(root string) error {
	// Check tmux is available
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is not installed")
	}

	skepBin, err := os.Executable()
	if err != nil {
		skepBin = "skep"
	}
	repoName := filepath.Base(root)
	sessionName := "skep-" + repoName

	// Kill any stale session with the same name so we always produce a
	// fresh, consistent "skep-<reponame>" debugging target.
	exec.Command("tmux", "kill-session", "-t", sessionName).Run()

	// Create a detached session rooted in the repo.
	create := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", root)
	if err := create.Run(); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Send the daemon command to the session's first pane via send-keys,
	// so the daemon runs as a foreground job in a real shell — guarantees
	// TMUX env var is set correctly when the daemon starts.
	daemonCmd := fmt.Sprintf("%s daemon", shellQuoteInit(skepBin))
	if err := exec.Command("tmux", "send-keys", "-t", sessionName+":0", daemonCmd, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}

	fmt.Printf("Daemon started in hidden tmux session '%s'\n", sessionName)
	fmt.Printf("Attach to debug:       tmux attach -t %s\n", sessionName)
	fmt.Printf("Detach (back to work): Ctrl+B d\n")
	fmt.Printf("Stop:                  skep daemon stop\n")
	return nil
}

// shellQuoteInit wraps a string in single quotes for safe shell use.
func shellQuoteInit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func promptWorkspace(root string, reader *bufio.Reader) string {
	fmt.Printf("Workspace groups related repos for cross-repo features.\n\n")

	parent := filepath.Dir(root)
	grandparent := filepath.Dir(parent)

	options := []string{parent}
	if grandparent != parent {
		options = append(options, grandparent)
	}

	for i, opt := range options {
		fmt.Printf("  %d. %s\n", i+1, opt)
	}
	fmt.Printf("  s. Skip (no cross-repo features)\n")
	fmt.Printf("\nWorkspace root [1]: ")

	input := readLine(reader)

	if input == "s" || input == "S" {
		return ""
	}

	choice := 0
	if input == "2" && len(options) > 1 {
		choice = 1
	}

	wsRoot := options[choice]
	registry.InitWorkspace(wsRoot)
	fmt.Printf("Workspace: %s\n", wsRoot)
	return wsRoot
}

func modelOptions(preset string) []string {
	switch preset {
	case "claude":
		return []string{"sonnet", "opus", "haiku"}
	case "gemini":
		return []string{"gemini-2.5-pro", "gemini-2.5-flash"}
	default:
		return nil
	}
}

func detectTestCmd(root string) string {
	// Go
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		return "go test ./..."
	}
	// Node.js — check package.json for test script
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		if strings.Contains(string(data), `"test"`) {
			return "npm test"
		}
	}
	// Python
	if _, err := os.Stat(filepath.Join(root, "pytest.ini")); err == nil {
		return "pytest"
	}
	if _, err := os.Stat(filepath.Join(root, "pyproject.toml")); err == nil {
		return "pytest"
	}
	// Rust
	if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err == nil {
		return "cargo test"
	}
	return ""
}

func addToGitignore(root string) {
	gitignorePath := filepath.Join(root, ".gitignore")
	data, _ := os.ReadFile(gitignorePath)
	content := string(data)

	if strings.Contains(content, ".skep") {
		return // already there
	}

	// Check if git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = root
	if cmd.Run() != nil {
		return // not a git repo
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		f.WriteString("\n")
	}
	f.WriteString("\n# skep\n.skep/\n")
}

func readLine(reader *bufio.Reader) string {
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}
