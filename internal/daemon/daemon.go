package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/config"
	"github.com/ChaitanyaPinapaka/skep/internal/graph"
	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/logfile"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
	"github.com/ChaitanyaPinapaka/skep/internal/tmuxutil"
)

const maxRequestBytes = 1 << 20 // 1 MB cap on socket request body

// Daemon is the per-repo agent — one worker bee per repo.
// Each Daemon tends exactly one hive (the `.skep/` directory on disk) and
// speaks to its peers in the apiary through a unix socket and MCP waggles.
// A daemon never dictates what its workers do; it holds the structure and
// forwards signals so the colony can self-organize.
type Daemon struct {
	Root     string
	SkepDir  string // the repo's .skep/ directory
	Store    *index.Store
	Config   *config.Config
	Listener net.Listener
	Lock     *os.File
	Term     *Terminal
	Log      *logfile.LogFile

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // tracks in-flight conn handlers
}

// Run starts the daemon: acquires lock, indexes, listens, polls task queue.
func Run(root, skepDir string) error {
	if err := os.MkdirAll(skepDir, 0o755); err != nil {
		return err
	}

	// Acquire flock — only one daemon per repo
	lock, err := AcquireLock(skepDir)
	if err != nil {
		return err
	}

	WritePID(skepDir)

	// Open store and index
	store, err := index.FullIndexAndRecord(root, skepDir)
	if err != nil {
		lock.Close()
		return fmt.Errorf("index: %w", err)
	}
	graph.ComputePageRank(store)

	cfg := config.Load(skepDir)

	// Start listener
	ln, err := Listen(skepDir, root)
	if err != nil {
		store.Close()
		lock.Close()
		return fmt.Errorf("listen: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Terminal manager — one task at a time per repo (serial execution)
	term := NewTerminal(1, cfg.TmuxLayout, nil)
	dlog := logfile.IndexLog(skepDir)

	d := &Daemon{
		Root:     root,
		SkepDir:  skepDir,
		Store:    store,
		Config:   cfg,
		Listener: ln,
		Lock:     lock,
		Term:     term,
		Log:      dlog,
		ctx:      ctx,
		cancel:   cancel,
	}

	// Crash recovery for the entire daemon
	defer logfile.RecoverWith(dlog, "daemon main loop")()

	// Sweep any tasks left in 'executing' from a prior crashed daemon.
	d.sweepStuckExecuting()

	// Set the onComplete callback now that d exists
	term.onComplete = func(taskID int) {
		// Re-index after any task window closes
		index.EnsureFresh(root, skepDir)
	}

	fc, _ := store.FileCount()
	sc, _ := store.SymbolCount()
	mode := detectMode()
	dlog.Log("daemon started: pid %d, %d files, %d symbols, terminal: %s", os.Getpid(), fc, sc, mode)
	fmt.Fprintf(os.Stderr, "skep daemon: indexed %d files, %d symbols\n", fc, sc)
	fmt.Fprintf(os.Stderr, "skep daemon: listening, pid %d, terminal: %s\n", os.Getpid(), mode)

	// Signal handling — graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Accept connections in background
	go d.acceptLoop()

	// Poll task queue in background
	go d.taskLoop()

	// Wait for signal or context cancel
	select {
	case <-sigCh:
	case <-ctx.Done():
	}

	return d.shutdown()
}

func (d *Daemon) shutdown() error {
	fmt.Fprintf(os.Stderr, "skep daemon: shutting down\n")
	d.cancel()
	d.Listener.Close()

	// Wait for in-flight connection handlers to finish (with timeout)
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		fmt.Fprintf(os.Stderr, "skep daemon: forcing shutdown after 5s\n")
	}

	d.Store.Close()
	Cleanup(d.SkepDir)
	CleanupListener(d.SkepDir, d.Root)
	d.Lock.Close()
	return nil
}

func (d *Daemon) acceptLoop() {
	for {
		conn, err := d.Listener.Accept()
		if err != nil {
			select {
			case <-d.ctx.Done():
				return
			default:
				// Backoff on accept errors to avoid busy loop
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}
		if err := verifyPeer(conn); err != nil {
			conn.Close()
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.handleConn(conn)
		}()
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	defer logfile.RecoverWith(d.Log, "socket connection")()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Limit request body to prevent OOM/DoS from a malicious or buggy client
	limited := io.LimitReader(conn, maxRequestBytes)

	var req Request
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		json.NewEncoder(conn).Encode(Response{Error: "invalid request"})
		return
	}

	resp := d.dispatch(req)
	json.NewEncoder(conn).Encode(resp)
}

func (d *Daemon) dispatch(req Request) Response {
	switch req.Cmd {
	case "status":
		fc, _ := d.Store.FileCount()
		sc, _ := d.Store.SymbolCount()
		return Response{OK: true, Data: map[string]interface{}{
			"files": fc, "symbols": sc, "root": d.Root, "pid": os.Getpid(),
			"active_tasks": d.Term.ActiveCount(), "queued_tasks": d.Term.QueueLen(),
		}}

	case "notify_task":
		// Daemon will pick it up on next poll cycle — just acknowledge
		return Response{OK: true}

	case "create_task":
		task, dedup, err := tasks.Create(d.Store, req.Description, req.SourceRepo, "")
		if err != nil {
			return Response{Error: err.Error()}
		}
		if dedup != nil && dedup.IsDuplicate {
			return Response{OK: true, Data: map[string]interface{}{"duplicate": true, "reason": dedup.Reason}}
		}
		return Response{OK: true, Data: map[string]interface{}{"task_id": task.ID}}

	case "get_task":
		if req.TaskID == 0 {
			return Response{Error: "task_id required"}
		}
		task, err := tasks.Get(d.Store, req.TaskID)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Data: task}

	case "list_tasks":
		taskList, err := tasks.List(d.Store)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Data: taskList}

	case "approve_task":
		if req.TaskID == 0 {
			return Response{Error: "task_id required"}
		}
		if err := tasks.Approve(d.Store, req.TaskID); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Data: map[string]interface{}{"task_id": req.TaskID, "status": "approved"}}

	case "stop":
		go func() {
			time.Sleep(100 * time.Millisecond)
			d.cancel()
		}()
		return Response{OK: true}

	default:
		return Response{Error: fmt.Sprintf("unknown command: %s", req.Cmd)}
	}
}

// sweepStuckExecuting flips any tasks left in 'executing' state from a
// previous daemon run to 'interrupted'. Without this, a crashed daemon
// leaves tasks wedged forever and `skep task run <id>` refuses them.
func (d *Daemon) sweepStuckExecuting() {
	stuck, err := tasks.ListByStatus(d.Store, tasks.StatusExecuting)
	if err != nil || len(stuck) == 0 {
		return
	}
	n := 0
	for _, t := range stuck {
		t.Status = tasks.StatusInterrupted
		t.Result = "orphaned: daemon restarted during execution"
		if err := tasks.Update(d.Store, t); err != nil {
			d.Log.Log("sweep: failed to update task #%d: %v", t.ID, err)
			continue
		}
		d.Log.Log("sweep: flipped task #%d from executing to interrupted", t.ID)
		n++
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "skep: swept %d orphaned executing tasks to interrupted\n", n)
	}
}

// taskLoop polls for tasks that need work: classify created tasks, execute approved tasks.
func (d *Daemon) taskLoop() {
	defer logfile.RecoverWith(d.Log, "task loop")()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Watchdog runs on its own ticker — faster than the classify/spawn
	// loop so users don't wait 3+ seconds to notice a pending prompt,
	// but not so fast that tmux capture-pane becomes the hot path.
	watchdogTicker := time.NewTicker(2 * time.Second)
	defer watchdogTicker.Stop()

	for {
		select {
		case <-ticker.C:
			d.processCreatedTasks()
			d.processApprovedTasks()
		case <-watchdogTicker.C:
			d.runApprovalWatchdog()
		case <-d.ctx.Done():
			return
		}
	}
}

// runApprovalWatchdog captures the tail of each executing task's tmux
// pane and pattern-matches against the approval-prompt regex list.
// On match: set task.NeedsInput, add "[!] " to the window name.
// On clear (no match on a task that previously had NeedsInput): unset
// the flag and strip the prefix.
//
// Best-effort: any tmux failure is silently dropped. The watchdog
// must never block or crash the daemon loop — a missed prompt is
// a UX bug, a panicked daemon is a data bug.
func (d *Daemon) runApprovalWatchdog() {
	if !d.Config.ApprovalWatchdogEnabled() {
		return
	}
	executing, err := tasks.ListByStatus(d.Store, tasks.StatusExecuting)
	if err != nil || len(executing) == 0 {
		return
	}
	patterns := tasks.ApprovalPatterns(d.Config.ApprovalPatterns)
	if len(patterns) == 0 {
		return
	}
	for _, t := range executing {
		target := findTmuxTargetByBranch(t.Branch)
		if target == "" {
			continue
		}
		pane := tmuxutil.CapturePaneTail(target, 30)
		matched := tasks.DetectApprovalPrompt(pane, patterns)
		if matched == t.NeedsInput {
			continue // state unchanged, nothing to persist
		}
		t.NeedsInput = matched
		if err := tasks.Update(d.Store, t); err != nil {
			continue
		}
		prefix := ""
		if matched {
			prefix = "[!]"
		}
		tmuxutil.SetWindowNamePrefix(target, prefix)
	}
}

// findTmuxTargetByBranch is a thin wrapper over tmuxutil so existing
// daemon.go callers don't need to change. All the real work lives in
// internal/tmuxutil — same function is reused by the CLI layer.
func findTmuxTargetByBranch(branch string) string {
	return tmuxutil.FindTargetByBranchName(branch)
}

// processCreatedTasks classifies tasks that are in 'created' status.
//
// Auto-approve policy for nested / cross-repo delegation:
//   - Small tasks auto-approve when AutoExecuteSmall is on (original intent).
//   - Tasks delivered via create_remote_task (SourceRepo != "") that classify
//     as "small" ALSO auto-approve, regardless of AutoExecuteSmall. Reason:
//     the sender explicitly picked this repo and is waiting via
//     wait_remote_task — blocking the whole chain on human approval of a
//     trivial delegate defeats the purpose.
//   - Large/ambiguous delegated tasks still require human approval on the
//     peer side. This is conservative until the needs_input back-channel
//     (future scope) lets the sender surface a pending approval.
func (d *Daemon) processCreatedTasks() {
	created, _ := tasks.ListByStatus(d.Store, tasks.StatusCreated)
	for _, task := range created {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		result, err := tasks.ClassifyAndPlanCtx(d.ctx, d.Store, d.Root, d.Config.ClassifyCmd(), d.Config.PlanCmd(), task)
		if err == nil && result != nil && result.Classification != "" && result.ClassifyErr == nil {
			task.Classification = result.Classification
			task.Plan = tasks.FormatPipelineResult(result)
			if len(result.Plan) > 0 {
				if raw, mErr := json.Marshal(result.Plan); mErr == nil {
					task.PlanStepsJSON = string(raw)
				}
			}

			switch {
			case result.Classification == "reject":
				task.Status = tasks.StatusRejected
				task.Result = result.RejectReason
			case result.NeedsClarification:
				// Daemon cannot prompt a human. Park the task and write
				// the clarify file — whoever opened the peer task is
				// responsible for discovering it via get_remote_task.
				task.Status = tasks.StatusPendingClarification
				if path, werr := tasks.WriteClarifyFile(d.SkepDir, task.ID, task.Description, result.ClarifyingQuestions); werr == nil {
					task.Result = "needs clarification: " + path
				}
			default:
				isSmall := result.Classification == "small"
				isDelegated := task.SourceRepo != ""
				if isSmall && (d.Config.AutoExecuteSmall || isDelegated) {
					task.Status = tasks.StatusApproved
				} else {
					task.Status = tasks.StatusPending
				}
			}
		} else {
			task.Status = tasks.StatusPending
			if result != nil && result.ClassifyErr != nil {
				task.Result = fmt.Sprintf("classify failed: %v", result.ClassifyErr)
			} else if err != nil {
				task.Result = fmt.Sprintf("pipeline failed: %v", err)
			}
		}
		tasks.Update(d.Store, task)
	}
}

// processApprovedTasks spawns tmux windows for approved tasks.
func (d *Daemon) processApprovedTasks() {
	approved, _ := tasks.ListByStatus(d.Store, tasks.StatusApproved)
	if len(approved) == 0 {
		return
	}

	// Spawn each approved task — Terminal handles concurrency and queueing
	for i := len(approved) - 1; i >= 0; i-- { // oldest first (list is DESC)
		task := approved[i]
		task.Status = tasks.StatusQueued
		tasks.Update(d.Store, task)

		if err := d.Term.Spawn(task.ID, task.Name); err != nil {
			fmt.Fprintf(os.Stderr, "skep daemon: task #%d: %v\n", task.ID, err)
		}
	}
}
