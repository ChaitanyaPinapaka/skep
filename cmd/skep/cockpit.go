package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cockpitConfig is the tmux snippet written to ~/.tmux.conf.d/skep.conf.
// It is sourced by the user's own ~/.tmux.conf — `skep cockpit` never
// edits ~/.tmux.conf itself.
const cockpitConfig = `# ═══════════════════════════════════════════════════════════════════════
# skep cockpit — managed by ` + "`skep cockpit setup`" + `
#
# To activate, add this line to your ~/.tmux.conf and reload:
#   source-file ~/.tmux.conf.d/skep.conf
#
# Remove with: skep cockpit reset
# ═══════════════════════════════════════════════════════════════════════

# ─── Prefix and general responsiveness ───────────────────────────────
# Faster escape — tmux's default 500ms eats vim/Claude TUI escape sequences.
set -sg escape-time 0
# Forward focus events to apps (vim autoread, Claude Code repaint on focus).
set -g focus-events on
# 256 colors + truecolor passthrough so Claude Code syntax highlighting
# actually renders the right colors instead of falling back to 16.
set -g default-terminal "tmux-256color"
set -ga terminal-overrides ",*256col*:Tc,xterm-256color:Tc,alacritty:Tc"
# Window/pane indices start at 1 to match the keyboard row.
set -g base-index 1
setw -g pane-base-index 1
# Renumber windows when one closes so Ctrl+b 1..9 keeps working.
set -g renumber-windows on
# Don't kill the session when the last window exits — the cockpit stays open.
set -g detach-on-destroy off
# Don't shrink panes when a smaller client attaches temporarily.
setw -g aggressive-resize on
# Long window names — tasks often have slugs plus the [!] watchdog prefix.
set -g status-left-length 30

# ─── Scrolling + mouse ───────────────────────────────────────────────
# Claude Code runs produce a LOT of output. Mouse + big scrollback
# are non-negotiable for the cockpit experience.
set -g mouse on
set -g history-limit 50000

# ─── Copy mode (vim keys) ────────────────────────────────────────────
# Users are already in vim mode in their editor; mode-keys vi makes
# copy mode consistent so muscle memory carries over.
setw -g mode-keys vi
bind -T copy-mode-vi v send -X begin-selection
bind -T copy-mode-vi y send -X copy-pipe-and-cancel
# Mouse drag select without auto-cancel so dragging doesn't wipe the
# selection the moment you release — matches how most GUIs behave.
bind -T copy-mode-vi MouseDragEnd1Pane send -X copy-pipe-no-clear

# ─── Pane navigation (vim style) ─────────────────────────────────────
bind h select-pane -L
bind j select-pane -D
bind k select-pane -U
bind l select-pane -R

# ─── Pane resize (held for continuous resize) ────────────────────────
bind -r H resize-pane -L 5
bind -r J resize-pane -D 5
bind -r K resize-pane -U 5
bind -r L resize-pane -R 5

# ─── Splits that preserve cwd ────────────────────────────────────────
# | and - are visually intuitive for horizontal/vertical splits and
# the -c flag keeps the new pane in the same directory as the parent.
bind | split-window -h -c "#{pane_current_path}"
bind - split-window -v -c "#{pane_current_path}"
bind c new-window -c "#{pane_current_path}"

# ─── Jump to task windows by number ──────────────────────────────────
bind 1 select-window -t :=1
bind 2 select-window -t :=2
bind 3 select-window -t :=3
bind 4 select-window -t :=4
bind 5 select-window -t :=5
bind 6 select-window -t :=6
bind 7 select-window -t :=7
bind 8 select-window -t :=8
bind 9 select-window -t :=9

# ─── Config reload ───────────────────────────────────────────────────
# One-touch reload is worth its weight when tuning the cockpit.
bind R source-file ~/.tmux.conf \; display-message "tmux config reloaded"

# ─── Cockpit actions (skep-specific popups + jumps) ─────────────────
# Popups float over the current view and vanish on exit — the whole
# "apiary on demand" experience hinges on these.
bind S display-popup -w 90% -h 80% -E 'skep workspace watch'
bind T display-popup -w 80% -h 50% -E 'skep tasks'
bind A display-popup -w 70% -h 60% -E 'skep index ask'
bind ! run-shell 'skep task jump-pending'

# ─── Activity monitoring for background task panes ──────────────────
# Tmux flags a window when its pane produces output. Combined with
# the approval watchdog, you can tell at a glance which foragers
# are actively working vs sitting on a prompt.
setw -g monitor-activity on
set -g visual-activity off
set -g visual-bell off
set -g bell-action any

# ─── Status line — fed by skep ───────────────────────────────────────
# Everything important about workspace state lives here: counts,
# approval bells, the clock. No permanent pane needed.
set -g status-interval 5
set -g status-position bottom
set -g status-justify left
set -g status-left ' ◠ '
set -g status-right '#(skep status --oneline) #[fg=colour244]%H:%M '
set -g status-right-length 120

# Understated styling — readable on dark and light terminal themes.
# If you already have a tmux theme you like, comment these out.
set -g status-style 'bg=colour234 fg=colour244'
set -g window-status-current-style 'fg=colour15 bg=colour238 bold'
set -g message-style 'bg=colour234 fg=colour15'
set -g pane-border-style 'fg=colour238'
set -g pane-active-border-style 'fg=colour244'
`

// cockpitPaths resolves the on-disk locations skep cockpit manages.
// Kept as a helper so setup/reset/status stay in lockstep.
func cockpitPaths() (home, dir, conf, userTmux string, err error) {
	home, err = os.UserHomeDir()
	if err != nil {
		return "", "", "", "", fmt.Errorf("skep cockpit: %w", err)
	}
	dir = filepath.Join(home, ".tmux.conf.d")
	conf = filepath.Join(dir, "skep.conf")
	userTmux = filepath.Join(home, ".tmux.conf")
	return home, dir, conf, userTmux, nil
}

// cmdCockpit dispatches `skep cockpit [verb]`. Bare form runs status.
func cmdCockpit(args []string) error {
	if len(args) == 0 {
		return cmdCockpitStatus(nil)
	}
	switch args[0] {
	case "setup":
		return cmdCockpitSetup(args[1:])
	case "reset":
		return cmdCockpitReset(args[1:])
	case "status":
		return cmdCockpitStatus(args[1:])
	}
	return usageErrorf("unknown cockpit verb '%s'. Expected: setup, reset, status.", args[0])
}

// cmdCockpitSetup writes ~/.tmux.conf.d/skep.conf, backing up any existing
// file first. It never touches the user's ~/.tmux.conf.
func cmdCockpitSetup(_ []string) error {
	_, dir, conf, _, err := cockpitPaths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("skep cockpit: %w", err)
	}
	if _, err := os.Stat(conf); err == nil {
		backup := fmt.Sprintf("%s.backup-%d", conf, time.Now().Unix())
		if err := os.Rename(conf, backup); err != nil {
			return fmt.Errorf("skep cockpit: backing up existing config: %w", err)
		}
	}
	if err := os.WriteFile(conf, []byte(cockpitConfig), 0o644); err != nil {
		return fmt.Errorf("skep cockpit: %w", err)
	}
	fmt.Printf(`skep cockpit: wrote ~/.tmux.conf.d/skep.conf

To activate, add this line to your ~/.tmux.conf (one-time):
  source-file ~/.tmux.conf.d/skep.conf

Then reload tmux:
  tmux source-file ~/.tmux.conf

Or if tmux isn't running yet, it'll pick it up on next start.
`)
	return nil
}

// cmdCockpitReset deletes ~/.tmux.conf.d/skep.conf. Backups are left alone.
func cmdCockpitReset(_ []string) error {
	_, _, conf, _, err := cockpitPaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(conf); os.IsNotExist(err) {
		fmt.Println("skep cockpit: nothing to remove (~/.tmux.conf.d/skep.conf does not exist)")
		return nil
	}
	if err := os.Remove(conf); err != nil {
		return fmt.Errorf("skep cockpit: %w", err)
	}
	fmt.Println("skep cockpit: removed ~/.tmux.conf.d/skep.conf")
	return nil
}

// cockpitStatusInfo is the --json shape for `skep cockpit status`.
type cockpitStatusInfo struct {
	ConfPath       string `json:"conf_path"`
	Installed      bool   `json:"installed"`
	Size           int64  `json:"size"`
	UserTmuxConf   string `json:"user_tmux_conf"`
	Sourced        bool   `json:"sourced"`
	UserTmuxExists bool   `json:"user_tmux_exists"`
}

// cmdCockpitStatus reports whether the skep-owned conf exists and whether
// the user's ~/.tmux.conf already sources it. Supports --json.
func cmdCockpitStatus(args []string) error {
	_, _, conf, userTmux, err := cockpitPaths()
	if err != nil {
		return err
	}
	info := cockpitStatusInfo{ConfPath: conf, UserTmuxConf: userTmux}
	if st, err := os.Stat(conf); err == nil {
		info.Installed = true
		info.Size = st.Size()
	}
	if b, err := os.ReadFile(userTmux); err == nil {
		info.UserTmuxExists = true
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "#") {
				continue
			}
			if strings.Contains(t, "source-file") && strings.Contains(t, "skep.conf") {
				info.Sourced = true
				break
			}
		}
	}
	jsonOrText(args, info, func() {
		if info.Installed {
			fmt.Printf("skep cockpit: installed at %s (%d bytes)\n", conf, info.Size)
		} else {
			fmt.Printf("skep cockpit: not installed (run 'skep cockpit setup')\n")
		}
		switch {
		case !info.UserTmuxExists:
			fmt.Printf("  ~/.tmux.conf: not found — create one and add 'source-file ~/.tmux.conf.d/skep.conf'\n")
		case info.Sourced:
			fmt.Printf("  ~/.tmux.conf: sources skep.conf (active)\n")
		default:
			fmt.Printf("  ~/.tmux.conf: does NOT source skep.conf — add 'source-file ~/.tmux.conf.d/skep.conf'\n")
		}
	})
	return nil
}
