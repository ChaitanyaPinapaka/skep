//go:build integration

// Package integration provides end-to-end test helpers for skep.
//
// These tests drive the real `skep` CLI binary against throwaway
// workspaces with a mocked Claude Code shell-out. They cover the
// full pipeline — dedup, classify, plan, execute, cross-repo
// delegation — without burning real LLM tokens and without
// requiring Claude Code to be installed on the test runner.
//
// Integration tests are gated behind a Go build tag so they never
// run as part of the default `go test ./...`. They are meaningfully
// slower than unit tests (a few seconds each, dominated by the
// one-time `skep` binary build in TestMain) and they depend on
// bash being on PATH for the mocked Claude shell script. Run them
// with:
//
//	go test -tags integration ./internal/integration/...
//
// or via the Makefile:
//
//	make test-integration
//
// Every test gets its own Workspace via NewWorkspace(t). The helper
// creates a temp directory with a .skep-workspace marker, writes a
// mock `claude` binary to a temp bin dir, and prepends that bin dir
// to PATH for the test's lifetime. When the test ends, t.Cleanup
// tears everything down.
//
// The mocked Claude binary is a small bash script that inspects the
// prompt it receives (via argv) and echoes a canned JSON response
// chosen by keyword — "CLASSIFIER" → classify.json, "PLANNER" →
// plan.json, etc. Tests configure responses via Workspace.MockClaude.
package integration
