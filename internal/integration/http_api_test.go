//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestHTTPAPI_status_endToEnd spawns a real daemon, hits the
// /v1/status endpoint with the bearer token from .skep/api.token,
// and asserts the response payload contains the expected fields.
//
// This is the v0.2.0 #2 acceptance test: the HTTP sidecar lives,
// the bearer token gates access, and the route table reaches the
// existing dispatch function unchanged.
func TestHTTPAPI_status_endToEnd(t *testing.T) {
	ws := NewWorkspace(t)
	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	addr, token := repo.StartDaemon()
	url := "http://" + addr + "/v1/status"

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}

	var got struct {
		OK   bool `json:"ok"`
		Data struct {
			Files   int    `json:"files"`
			Symbols int    `json:"symbols"`
			Root    string `json:"root"`
			Pid     int    `json:"pid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, body)
	}
	if !got.OK {
		t.Fatalf("response ok=false, body=%s", body)
	}
	if got.Data.Pid == 0 {
		t.Errorf("pid = 0, want a real pid")
	}
	if !strings.HasSuffix(got.Data.Root, "backend") {
		t.Errorf("root = %q, expected to end in 'backend'", got.Data.Root)
	}
}

// TestHTTPAPI_status_rejectsMissingToken is the negative test:
// hitting /v1/status with no Authorization header must return 401.
func TestHTTPAPI_status_rejectsMissingToken(t *testing.T) {
	ws := NewWorkspace(t)
	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	addr, _ := repo.StartDaemon()
	url := "http://" + addr + "/v1/status"

	resp, err := newClient().Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 401, body=%s", resp.StatusCode, body)
	}
}

// TestHTTPAPI_status_rejectsWrongToken: 401 on a wrong bearer.
func TestHTTPAPI_status_rejectsWrongToken(t *testing.T) {
	ws := NewWorkspace(t)
	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	addr, _ := repo.StartDaemon()

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 401, body=%s", resp.StatusCode, body)
	}
}

// TestHTTPAPI_listTasks_emptyRepo: GET /v1/tasks against a fresh
// repo with no tasks returns an OK response with an empty (or
// nil) data array.
func TestHTTPAPI_listTasks_emptyRepo(t *testing.T) {
	ws := NewWorkspace(t)
	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	addr, token := repo.StartDaemon()
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
}

// TestHTTPAPI_createTask_endToEnd posts a task to /v1/tasks and
// asserts the daemon's create_task path runs to completion. Uses
// the default mock claude responses seeded by NewWorkspace so the
// test does not have to stub the pipeline by hand.
func TestHTTPAPI_createTask_endToEnd(t *testing.T) {
	ws := NewWorkspace(t)
	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	addr, token := repo.StartDaemon()
	url := "http://" + addr + "/v1/tasks"

	body := bytes.NewReader([]byte(`{"description":"add hello world endpoint"}`))
	req, _ := http.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, respBody)
	}

	var got struct {
		OK   bool `json:"ok"`
		Data struct {
			TaskID int `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &got); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, respBody)
	}
	if !got.OK || got.Data.TaskID == 0 {
		t.Fatalf("expected ok=true and task_id>0, got body=%s", respBody)
	}

	// Verify the task eventually transitions from created → pending.
	// Uses the Eventually variant because the daemon's create_task
	// handler only inserts the row; the classify+plan pipeline
	// runs in the next tick of processCreatedTasks (~3s period).
	repo.AssertTaskStatusEventually(got.Data.TaskID, "pending")
}

// TestHTTPAPI_unknownTaskAction returns 404 when a path includes
// an unknown action verb under /v1/tasks/<id>/.
func TestHTTPAPI_unknownTaskAction(t *testing.T) {
	ws := NewWorkspace(t)
	repo := ws.NewRepo("backend", map[string]string{
		"go.mod":  "module example.com/backend\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})

	addr, token := repo.StartDaemon()
	url := fmt.Sprintf("http://%s/v1/tasks/1/jazzhands", addr)

	req, _ := http.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404, body=%s", resp.StatusCode, body)
	}
}

// newClient returns an http.Client with a short timeout suitable
// for integration tests against a localhost daemon.
func newClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}
