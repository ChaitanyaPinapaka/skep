package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/ChaitanyaPinapaka/skep/internal/daemon"
	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/registry"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
)

// minWatchInterval is the smallest polling interval we'll honor. Anything
// shorter degenerates into a busy loop that hammers SQLite for no user
// benefit.
const minWatchInterval = 500 * time.Millisecond

// watcher holds state that lives for the duration of a single
// `skep workspace watch` invocation. The store cache lets us avoid the
// open/close cycle on every tick, which matters on WSL2 where SQLite file
// handle churn is measurably slow.
type watcher struct {
	stores map[string]*index.Store // key: repo name
}

func newWatcher() *watcher {
	return &watcher{stores: make(map[string]*index.Store)}
}

// closeAll releases every cached store. Safe to call multiple times.
func (w *watcher) closeAll() {
	for name, s := range w.stores {
		if s != nil {
			s.Close()
		}
		delete(w.stores, name)
	}
}

// storeFor returns a cached store for the given repo, opening it on first
// access. Returns nil if the db does not exist or fails to open; caller
// should skip the repo silently in that case.
func (w *watcher) storeFor(name, dbPath string) *index.Store {
	if s, ok := w.stores[name]; ok {
		return s
	}
	s, err := index.OpenStore(dbPath)
	if err != nil {
		return nil
	}
	w.stores[name] = s
	return s
}

// drop evicts a cached store — used when a call against it fails, so the
// next tick re-opens it fresh.
func (w *watcher) drop(name string) {
	if s, ok := w.stores[name]; ok {
		if s != nil {
			s.Close()
		}
		delete(w.stores, name)
	}
}

// cmdWorkspaceWatch renders a live dashboard of every task across every repo
// in the workspace. Pure read — it opens each repo's index.db in read-only
// mode on each tick. No daemon dependency.
//
// The intent is that a user opens a dedicated tmux pane running this command
// and leaves it up all day as their workspace cockpit.
func cmdWorkspaceWatch(args []string) error {
	interval := 2 * time.Second
	once := false
	for i, a := range args {
		switch a {
		case "--once":
			once = true
		case "--interval":
			if i+1 < len(args) {
				if d, err := time.ParseDuration(args[i+1]); err == nil {
					interval = d
				}
			}
		}
	}
	// Floor the interval so '--interval 0' (or any absurdly small value)
	// can't busy-loop the ticker and thrash SQLite.
	if interval < minWatchInterval {
		interval = minWatchInterval
	}

	reg, err := registry.Load()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	if len(reg.Agents) == 0 {
		return fmt.Errorf("no repos registered in this workspace — run 'skep init' in each repo first")
	}

	w := newWatcher()
	defer w.closeAll()

	// render takes a registry; the watcher (and its store cache) is
	// captured via closure so the existing render(reg) call sites stay
	// terse while still benefiting from the cache.
	renderFn := func(r *registry.Registry) {
		render(r, w)
	}

	if once {
		renderFn(reg)
		return nil
	}

	// Install SIGINT/SIGTERM handler so Ctrl+C exits cleanly with the cursor
	// and alternate screen restored.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Enter alternate screen, hide cursor. Restore on exit.
	enterAltScreen()
	defer exitAltScreen()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	renderFn(reg)
	for {
		select {
		case <-ticker.C:
			// Re-load the registry each tick so newly-registered repos appear
			// without requiring the user to restart the dashboard.
			if r, err := registry.Load(); err == nil && r != nil {
				reg = r
			}
			renderFn(reg)
		case <-sigCh:
			return nil
		}
	}
}

// terminalWidth returns the current terminal width in columns, falling back
// to a sensible default when stdout isn't a TTY (e.g., piped to a file) or
// the ioctl fails. 120 is wide enough that existing fixed columns still fit.
func terminalWidth() int {
	const fallback = 120
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return fallback
	}
	return w
}

// layout holds the column widths chosen for a given terminal width. The
// fixed-width columns (ID/STATUS/CLASS/AGE/TOKENS) never change; REPO and
// BRANCH flex to fill the rest.
type layout struct {
	width  int
	repo   int
	name   int
	branch int
}

// computeLayout picks column widths for the given terminal width. Rules:
//   - keep ID/STATUS/CLASS/AGE/TOKENS fixed
//   - REPO capped at width*0.35, BRANCH at width*0.25, minimum 20 each
//   - NAME takes whatever remains after fixed + REPO + BRANCH + separators
func computeLayout(width int) layout {
	if width < 80 {
		width = 80
	}
	// Fixed columns: ID(4) STATUS(11) CLASS(10) AGE(8) TOKENS(9) = 42
	// Plus 2 leading spaces + 7 two-space separators = 16
	// Fixed overhead = 58. Remainder is shared by REPO + NAME + BRANCH.
	const fixedOverhead = 58
	remaining := width - fixedOverhead
	if remaining < 60 {
		remaining = 60
	}

	repoMax := int(float64(width) * 0.35)
	branchMax := int(float64(width) * 0.25)
	if repoMax < 20 {
		repoMax = 20
	}
	if branchMax < 20 {
		branchMax = 20
	}

	// Split remaining: REPO gets up to repoMax, BRANCH up to branchMax,
	// NAME gets the rest (at least 20).
	repo := repoMax
	branch := branchMax
	name := remaining - repo - branch
	if name < 20 {
		// Squeeze repo/branch proportionally.
		name = 20
		extra := remaining - name
		if extra < 40 {
			repo = extra / 2
			branch = extra - repo
		} else {
			repo = extra / 2
			branch = extra - repo
			if repo > repoMax {
				repo = repoMax
			}
			if branch > branchMax {
				branch = branchMax
			}
		}
	}
	if repo < 20 {
		repo = 20
	}
	if branch < 20 {
		branch = 20
	}

	return layout{width: width, repo: repo, name: name, branch: branch}
}

// render clears the screen and redraws the dashboard in place.
func render(reg *registry.Registry, w *watcher) {
	clearScreen()

	rows := collectRows(reg, w)

	// Header with timestamp + workspace summary.
	repoCount := len(reg.Agents)
	totalTasks := len(rows)
	active := 0
	pending := 0
	needClarify := 0
	for _, r := range rows {
		switch r.Status {
		case tasks.StatusExecuting, tasks.StatusQueued, tasks.StatusApproved:
			active++
		case tasks.StatusPending, tasks.StatusCreated, tasks.StatusClassified:
			pending++
		case tasks.StatusPendingClarification:
			needClarify++
		}
	}

	fmt.Printf("\x1b[1m◠  skep apiary\x1b[0m  —  %s\n",
		time.Now().Format("15:04:05"))
	clarifySeg := ""
	if needClarify > 0 {
		clarifySeg = fmt.Sprintf("  •  %s%d needs clarification\x1b[0m", colorFor(tasks.StatusPendingClarification), needClarify)
	}
	fmt.Printf("%d hives  •  %d tasks in flight  •  %s%d foraging\x1b[0m  •  %s%d waiting\x1b[0m%s\n\n",
		repoCount, totalTasks,
		colorFor(tasks.StatusExecuting), active,
		colorFor(tasks.StatusPending), pending,
		clarifySeg)

	if len(rows) == 0 {
		fmt.Println("  (the comb is empty — start a task with: skep task create \"...\")")
		return
	}

	// Column widths flex with the current terminal size so the table never
	// wraps on narrow panes and doesn't leave dead space on wide ones.
	lay := computeLayout(terminalWidth())

	headerFmt := fmt.Sprintf("  %%-4s  %%-%ds  %%-%ds  %%-11s  %%-10s  %%-8s  %%-9s  %%s\n",
		lay.repo, lay.name)
	rowFmt := fmt.Sprintf("  %%-4d  %%-%ds  %%-%ds  %%s%%-11s\x1b[0m  %%-10s  %%-8s  %%-9s  %%s\n",
		lay.repo, lay.name)

	fmt.Printf(headerFmt, "ID", "REPO", "NAME", "STATUS", "CLASS", "AGE", "TOKENS", "BRANCH")
	fmt.Printf("  %s\n", strings.Repeat("─", lay.width-2))

	var totalTokens int
	for _, r := range rows {
		age := shortAge(r.CreatedAt)
		class := r.Classification
		if class == "" {
			class = "-"
		}
		branch := r.Branch
		if branch == "" {
			branch = "-"
		}
		tokens := "-"
		if r.TokensUsed > 0 {
			tokens = shortTokens(r.TokensUsed)
			totalTokens += r.TokensUsed
		}
		fmt.Printf(rowFmt,
			r.ID,
			truncate(r.Repo, lay.repo),
			truncate(r.Name, lay.name),
			colorFor(r.Status),
			r.Status,
			class,
			age,
			tokens,
			truncate(branch, lay.branch),
		)
	}
	if totalTokens > 0 {
		fmt.Printf("\n  \x1b[2mTotal effective billed input tokens across workspace: %s\x1b[0m\n", shortTokens(totalTokens))
	}

	fmt.Println()
	fmt.Printf("  \x1b[2mCtrl+C to exit  •  refreshes every 2s  •  'skep task show <id>' for details\x1b[0m\n")
}

// row is a flattened view of one task across all repos.
type row struct {
	Repo           string
	ID             int
	Name           string
	Status         string
	Classification string
	Branch         string
	TokensUsed     int
	CreatedAt      time.Time
}

// collectRows fetches tasks from each repo's cached SQLite store. Stores
// are opened lazily on first sight and kept alive across ticks via the
// watcher; a failed call drops the store so the next tick re-opens it.
// Silently skips repos that aren't yet initialized or whose DB can't open.
func collectRows(reg *registry.Registry, w *watcher) []row {
	var all []row
	// Track which repos we saw this tick so we can evict stores for repos
	// that were removed from the registry.
	seen := make(map[string]struct{}, len(reg.Agents))
	for name, agent := range reg.Agents {
		if agent == nil {
			continue
		}
		seen[name] = struct{}{}
		dbPath := filepath.Join(agent.Path, ".skep", "index.db")
		if _, err := os.Stat(dbPath); err != nil {
			// DB not there (repo not initialized yet) — make sure we
			// don't hold a stale handle from a previous tick.
			w.drop(name)
			continue
		}
		store := w.storeFor(name, dbPath)
		if store == nil {
			continue
		}
		taskList, err := tasks.List(store)
		if err != nil {
			// Most likely the db was rewritten or corrupted — drop and
			// retry on the next tick.
			w.drop(name)
			continue
		}
		for _, t := range taskList {
			all = append(all, row{
				Repo:           name,
				ID:             t.ID,
				Name:           t.Name,
				Status:         t.Status,
				Classification: t.Classification,
				Branch:         t.Branch,
				TokensUsed:     t.TokensUsed,
				CreatedAt:      t.CreatedAt,
			})
		}
	}

	// Evict stores whose repos disappeared from the registry since the
	// previous tick, so we don't leak handles after 'skep init --remove'.
	for name := range w.stores {
		if _, ok := seen[name]; !ok {
			w.drop(name)
		}
	}

	// Sort so active work is at the top, then newest first within a status.
	statusRank := func(s string) int {
		switch s {
		case tasks.StatusExecuting:
			return 0
		case tasks.StatusQueued:
			return 1
		case tasks.StatusApproved:
			return 2
		case tasks.StatusPending, tasks.StatusClassified:
			return 3
		case tasks.StatusCreated:
			return 4
		case tasks.StatusInterrupted:
			return 5
		case tasks.StatusFailed:
			return 6
		case tasks.StatusDone:
			return 7
		case tasks.StatusRejected:
			return 8
		}
		return 9
	}
	sort.SliceStable(all, func(i, j int) bool {
		ri := statusRank(all[i].Status)
		rj := statusRank(all[j].Status)
		if ri != rj {
			return ri < rj
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	return all
}

func colorFor(status string) string {
	switch status {
	case tasks.StatusExecuting, tasks.StatusQueued:
		return "\x1b[1;33m" // yellow bold
	case tasks.StatusApproved:
		return "\x1b[1;34m" // blue bold
	case tasks.StatusPending, tasks.StatusCreated, tasks.StatusClassified:
		return "\x1b[36m" // cyan
	case tasks.StatusPendingClarification:
		return "\x1b[1;93m" // bright yellow bold — needs user attention
	case tasks.StatusDone:
		return "\x1b[32m" // green
	case tasks.StatusFailed:
		return "\x1b[1;31m" // red bold
	case tasks.StatusInterrupted:
		return "\x1b[35m" // magenta
	case tasks.StatusRejected:
		return "\x1b[2m" // dim
	}
	return ""
}

func shortAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// shortTokens formats a token count compactly (1.2k, 34k, 1.5M) so the
// dashboard column stays narrow.
func shortTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 2 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func enterAltScreen() {
	// \x1b[?1049h  enter alt screen
	// \x1b[?25l    hide cursor
	fmt.Print("\x1b[?1049h\x1b[?25l")
}

func exitAltScreen() {
	// \x1b[?25h    show cursor
	// \x1b[?1049l  leave alt screen
	fmt.Print("\x1b[?25h\x1b[?1049l")
}

func clearScreen() {
	// Move cursor to home, clear from cursor to end of screen.
	fmt.Print("\x1b[H\x1b[2J")
}

// unused vars to make the daemon import resolve until we wire it in;
// remove when the dashboard grows a "daemon running?" indicator column.
var _ = daemon.IsRunning
