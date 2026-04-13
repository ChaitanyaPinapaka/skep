//go:build integration

package integration

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// skepBinary is the path to the skep binary built once in TestMain
// and reused across every test in the package. Built into a shared
// temp dir so package-wide setup is cheap compared to per-test
// binary builds.
var (
	skepBinary string
	buildOnce  sync.Once
	buildErr   error
)

// BuildSkep compiles the skep binary to a temp dir the first time
// it is called and returns the absolute path on subsequent calls.
// Safe to call from TestMain or from the first NewWorkspace in a
// test run.
func BuildSkep(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "skep-integration-bin-*")
		if err != nil {
			buildErr = fmt.Errorf("mkdir build dir: %w", err)
			return
		}
		bin := filepath.Join(dir, "skep")
		// Compile against the current working tree. go build picks
		// up whatever state the developer has, which is exactly what
		// we want — integration tests should fail loudly when the
		// code they test regresses.
		repoRoot, err := findRepoRoot()
		if err != nil {
			buildErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/skep")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("go build skep: %w: %s", err, out.String())
			return
		}
		skepBinary = bin
	})
	if buildErr != nil {
		t.Fatalf("%v", buildErr)
	}
	return skepBinary
}

// findRepoRoot walks up from the package directory looking for the
// go.mod that belongs to github.com/ChaitanyaPinapaka/skep so
// `go build ./cmd/skep` has the right working directory.
func findRepoRoot() (string, error) {
	// Start from the source file's directory so `go test` run
	// from any sub-package still finds the right root.
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod walking up from %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// MockResponses holds the canned JSON payloads the fake Claude
// binary returns. Tests can set any subset — empty fields fall back
// to a safe default so single-shot tests don't have to stub every
// pipeline stage.
type MockResponses struct {
	// Classify is returned when the prompt looks like the classifier
	// (matches the "CLASSIFIER" token from classify.tmpl). Must parse
	// as tasks.ClassificationResult.
	Classify string
	// Plan is returned when the prompt looks like plan-gen (matches
	// "PLANNER" from plan.tmpl). Must parse as tasks.PlanResult.
	Plan string
	// Dedup is returned when the prompt looks like an LLM dedup
	// escape-hatch call. Defaults to a non-duplicate response.
	Dedup string
}

// Workspace is a throwaway skep workspace on disk. Every test gets
// its own so tests can run in parallel without stepping on each
// other's sockets, registries, or databases.
//
// The zero value is not useful; callers must go through NewWorkspace.
type Workspace struct {
	Root    string // workspace root containing .skep-workspace/
	BinDir  string // temp bin dir prepended to PATH (holds mock claude)
	MockDir string // where mock canned responses live
	Bin     string // path to the real skep binary built in TestMain

	t       *testing.T
	oldPath string
	repos   []*Repo
}

// NewWorkspace creates an empty workspace rooted in a temp directory,
// writes the workspace marker, seeds a mock Claude binary with
// default non-duplicate / reject responses, and prepends the mock
// bin dir to PATH. Everything is cleaned up via t.Cleanup.
func NewWorkspace(t *testing.T) *Workspace {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("integration harness uses a bash shell script for mock claude; windows not supported")
	}

	bin := BuildSkep(t)
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, ".skep-workspace"), 0o755); err != nil {
		t.Fatalf("mkdir workspace marker: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".skep-workspace", "registry.json"),
		[]byte(`{"agents":{}}`),
		0o644,
	); err != nil {
		t.Fatalf("seed empty registry: %v", err)
	}

	binDir := filepath.Join(root, ".bin")
	mockDir := filepath.Join(root, ".mock")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.MkdirAll(mockDir, 0o755); err != nil {
		t.Fatalf("mkdir mock: %v", err)
	}

	ws := &Workspace{
		Root:    root,
		BinDir:  binDir,
		MockDir: mockDir,
		Bin:     bin,
		t:       t,
		oldPath: os.Getenv("PATH"),
	}

	// Seed default safe responses so tests that forget to configure
	// the mock still produce predictable behavior rather than a
	// cryptic shell error.
	ws.MockClaude(MockResponses{
		Classify: `{"classification":"small","confidence":0.9,"reason":"mock default","needs_clarification":false}`,
		Plan:     `{"plan":[{"verb":"add","target_file":"main.go","symbols":[],"acceptance":"compiles","description":"mock plan step"}]}`,
		Dedup:    `{"is_duplicate":false}`,
	})

	os.Setenv("PATH", binDir+string(os.PathListSeparator)+ws.oldPath)

	t.Cleanup(func() {
		os.Setenv("PATH", ws.oldPath)
	})

	return ws
}

// MockClaude (re)writes the mock `claude` script and its canned
// response files. Tests can call this mid-run to switch responses
// between steps — useful when exercising clarify re-run flows or
// multi-stage pipelines.
//
// Empty fields fall back to safe defaults so tests only need to
// stub the stages they care about.
func (w *Workspace) MockClaude(resp MockResponses) {
	w.t.Helper()

	if resp.Classify == "" {
		resp.Classify = `{"classification":"small","confidence":0.9,"reason":"mock default","needs_clarification":false}`
	}
	if resp.Plan == "" {
		resp.Plan = `{"plan":[{"verb":"add","target_file":"main.go","symbols":[],"acceptance":"compiles","description":"mock plan step"}]}`
	}
	if resp.Dedup == "" {
		resp.Dedup = `{"is_duplicate":false}`
	}

	for name, body := range map[string]string{
		"classify.json": resp.Classify,
		"plan.json":     resp.Plan,
		"dedup.json":    resp.Dedup,
	} {
		if err := os.WriteFile(filepath.Join(w.MockDir, name), []byte(body), 0o644); err != nil {
			w.t.Fatalf("write mock %s: %v", name, err)
		}
	}

	// The mock script inspects argv for keywords from the real
	// prompt templates and echoes the matching canned response.
	// classify.tmpl uses "You are the CLASSIFIER"; plan.tmpl uses
	// "You are the PLANNER"; dedup.tmpl uses "duplicate" phrases.
	// If no pattern matches we fall back to classify so a test
	// that only stubs one stage still produces useful output.
	script := fmt.Sprintf(`#!/bin/bash
# Mock claude binary for skep integration tests. Owned by the test
# harness in internal/integration/harness.go — do not edit by hand.
MOCK_DIR=%q
PROMPT="$*"

if echo "$PROMPT" | grep -qi "you are the classifier\|CLASSIFIER"; then
    cat "$MOCK_DIR/classify.json"
elif echo "$PROMPT" | grep -qi "you are the planner\|PLANNER"; then
    cat "$MOCK_DIR/plan.json"
elif echo "$PROMPT" | grep -qi "duplicate.*tasks\|are these tasks.*duplicate"; then
    cat "$MOCK_DIR/dedup.json"
else
    # Default fallback: classify response. Keeps single-stub tests
    # working without forcing every test to stub the full pipeline.
    cat "$MOCK_DIR/classify.json"
fi
`, w.MockDir)

	scriptPath := filepath.Join(w.BinDir, "claude")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		w.t.Fatalf("write mock claude script: %v", err)
	}
}

// Repo is a single skep-managed repo inside the workspace. Tests
// create a Repo via Workspace.NewRepo, then drive it through the
// skep CLI via helper methods.
type Repo struct {
	Name    string
	Path    string // absolute path to the repo root
	SkepDir string // absolute path to .skep/

	ws *Workspace
	t  *testing.T
}

// NewRepo materializes a fresh repo under the workspace. Pass
// name="my-backend" and files={"main.go": "...", "auth/api.go": "..."}
// to lay down a minimal source tree, then call Init to run `skep init`
// against it.
//
// The files map accepts slash-separated keys; parent directories are
// created on demand. An empty map creates a valid-but-empty repo.
func (w *Workspace) NewRepo(name string, files map[string]string) *Repo {
	w.t.Helper()

	path := filepath.Join(w.Root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		w.t.Fatalf("mkdir repo %s: %v", name, err)
	}

	for rel, body := range files {
		abs := filepath.Join(path, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			w.t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			w.t.Fatalf("write %s: %v", abs, err)
		}
	}

	// `skep init` is interactive; for tests we stand up the minimum
	// state it would produce — a .skep/ directory with a config.json
	// pointing at the claude preset and no daemon — and let the real
	// `skep index create` populate index.db. That path is headless
	// and covers every indexer code path the classifier depends on.
	skepDir := filepath.Join(path, ".skep")
	if err := os.MkdirAll(skepDir, 0o755); err != nil {
		w.t.Fatalf("mkdir .skep: %v", err)
	}
	configJSON := `{"llm":"claude","tmux_layout":"split-h"}`
	if err := os.WriteFile(filepath.Join(skepDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		w.t.Fatalf("write config.json: %v", err)
	}

	r := &Repo{
		Name:    name,
		Path:    path,
		SkepDir: skepDir,
		ws:      w,
		t:       w.t,
	}
	w.repos = append(w.repos, r)

	// Build the index. The real skep binary runs the full tree-sitter
	// pipeline against whatever files we laid down above.
	r.MustRun("index", "create")

	return r
}

// Run executes the skep binary in the repo's directory with the
// given args and returns (stdout+stderr, error). Never panics.
// Prefer MustRun when you want a failing test on non-zero exit.
func (r *Repo) Run(args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command(r.ws.Bin, args...)
	cmd.Dir = r.Path
	// PATH already has our mock bin dir prepended by NewWorkspace.
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// MustRun is Run but fails the test on non-zero exit. Returns the
// captured output for the caller to inspect.
func (r *Repo) MustRun(args ...string) string {
	r.t.Helper()
	out, err := r.Run(args...)
	if err != nil {
		r.t.Fatalf("skep %s (in %s) failed: %v\n---output---\n%s", strings.Join(args, " "), r.Path, err, out)
	}
	return out
}

// CreateTask runs `skep task create <description>` and returns the
// task id extracted from the output. Fails the test if the command
// errors or the id cannot be parsed.
//
// The classify + plan pipeline shells out to the mock Claude binary
// on PATH, so tests must have called Workspace.MockClaude (or
// accepted the defaults seeded by NewWorkspace) before calling this.
func (r *Repo) CreateTask(description string) int {
	r.t.Helper()
	out := r.MustRun("task", "create", description)
	// Parse "#<id>" from the first line of the output. `skep task create`
	// currently prints e.g. "#7 add-health-endpoint [small]" on success.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			fields := strings.Fields(line)
			if len(fields) < 1 {
				continue
			}
			idStr := strings.TrimPrefix(fields[0], "#")
			id, err := strconv.Atoi(idStr)
			if err == nil {
				return id
			}
		}
	}
	r.t.Fatalf("could not parse task id from output:\n%s", out)
	return 0
}

// TaskStatus reads the current status of a task directly from the
// repo's index.db via a raw SQL query. Bypasses the CLI so assertions
// don't have to parse prose output.
func (r *Repo) TaskStatus(id int) string {
	r.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(r.SkepDir, "index.db"))
	if err != nil {
		r.t.Fatalf("open index.db: %v", err)
	}
	defer db.Close()

	var status string
	if err := db.QueryRow("SELECT status FROM tasks WHERE id = ?", id).Scan(&status); err != nil {
		r.t.Fatalf("query task %d: %v", id, err)
	}
	return status
}

// AssertTaskStatus fails the test if task <id>'s status is not want.
// Polls briefly to account for async classify+plan work — the CLI
// returns after the task row is written but some status transitions
// (e.g. daemon-driven) happen out of band.
func (r *Repo) AssertTaskStatus(id int, want string) {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = r.TaskStatus(id)
		if last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("task %d: status = %q, want %q", id, last, want)
}
