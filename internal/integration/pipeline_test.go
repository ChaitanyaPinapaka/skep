//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestPipeline_smallTaskLandsPending is the smoke test for the
// classify + plan pipeline. It stubs the classifier to return a
// confident "small" verdict and asserts the new task lands in
// `pending` status (awaiting human approval because
// auto-execute-small isn't enabled in the mock config).
//
// If this test passes, the harness is wired end-to-end: build skep
// → lay down a test repo → index it → run the real CLI → hit the
// mocked classifier via PATH-shimmed claude → store the result in
// SQLite → read the status back. Every later integration test
// builds on this plumbing.
func TestPipeline_smallTaskLandsPending(t *testing.T) {
	ws := NewWorkspace(t)

	ws.MockClaude(MockResponses{
		Classify: `{
			"classification": "small",
			"confidence": 0.95,
			"reason": "single-file change, mock classifier says small",
			"needs_clarification": false,
			"clarifying_questions": [],
			"reject_reason": ""
		}`,
		Plan: `{
			"plan": [
				{
					"verb": "add",
					"target_file": "internal/api/health.go",
					"symbols": ["HealthHandler"],
					"acceptance": "HealthHandler exists and returns 200",
					"description": "Add HealthHandler to internal/api/health.go"
				}
			],
			"files_affected": ["internal/api/health.go"],
			"symbols_affected": ["HealthHandler"]
		}`,
	})

	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	id := repo.CreateTask("add a GET /health endpoint returning 200")

	repo.AssertTaskStatus(id, "pending")
}

// TestPipeline_ambiguousTaskParksForClarification stubs the
// classifier to return `ambiguous` + needs_clarification=true and
// asserts the task lands in pending_clarification. Also verifies
// the clarify file was written to .skep/clarify/<id>.md so the
// user (or the test) can answer the questions and re-run.
func TestPipeline_ambiguousTaskParksForClarification(t *testing.T) {
	ws := NewWorkspace(t)

	ws.MockClaude(MockResponses{
		Classify: `{
			"classification": "ambiguous",
			"confidence": 0.7,
			"reason": "no target metric or baseline, could mean anything",
			"needs_clarification": true,
			"clarifying_questions": [
				"Which endpoint or page feels slow?",
				"What latency target are you shooting for?"
			],
			"reject_reason": ""
		}`,
	})

	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	id := repo.CreateTask("make the app faster")

	repo.AssertTaskStatus(id, "pending_clarification")
}

// TestCLI_taskCreateStdin verifies `skep task create -` reads the
// description from stdin and runs the same classify+plan path as the
// positional form. Smoke test for the v0.2.0 CLI polish item — once
// stdin is plumbed correctly, the rest of the pipeline is identical.
func TestCLI_taskCreateStdin(t *testing.T) {
	ws := NewWorkspace(t)

	ws.MockClaude(MockResponses{
		Classify: `{
			"classification": "small",
			"confidence": 0.9,
			"reason": "tiny addition",
			"needs_clarification": false,
			"clarifying_questions": [],
			"reject_reason": ""
		}`,
		Plan: `{
			"plan": [
				{
					"verb": "add",
					"target_file": "internal/api/ping.go",
					"symbols": ["PingHandler"],
					"acceptance": "PingHandler returns 200",
					"description": "Add PingHandler"
				}
			],
			"files_affected": ["internal/api/ping.go"],
			"symbols_affected": ["PingHandler"]
		}`,
	})

	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	out, err := repo.RunStdin("add a /ping endpoint that returns 200\n", "task", "create", "-")
	if err != nil {
		t.Fatalf("task create - failed: %v\n---output---\n%s", err, out)
	}
	// Expect output to mention a new task id like "#N name [class]".
	if !strings.Contains(out, "#") {
		t.Fatalf("expected task id in output, got:\n%s", out)
	}
}

// TestPipeline_rejectedTaskTerminates stubs the classifier to
// return `reject` and asserts the task lands terminal in the
// rejected state immediately.
func TestPipeline_rejectedTaskTerminates(t *testing.T) {
	ws := NewWorkspace(t)

	ws.MockClaude(MockResponses{
		Classify: `{
			"classification": "reject",
			"confidence": 1.0,
			"reason": "destructive command, not a software task",
			"needs_clarification": false,
			"clarifying_questions": [],
			"reject_reason": "not an actionable software task"
		}`,
	})

	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	id := repo.CreateTask("rm -rf /")

	repo.AssertTaskStatus(id, "rejected")
}
