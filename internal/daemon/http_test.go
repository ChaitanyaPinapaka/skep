package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// httpTestDaemon returns a Daemon stub good enough for the HTTP
// route tests — no store, no listener, no terminal. Each handler
// is exercised against the route mux directly via httptest, so
// the unit test never touches sockets or SQLite.
//
// Real end-to-end coverage of the HTTP API against a live store
// lives in internal/integration/.
func httpTestDaemon() *Daemon {
	return &Daemon{}
}

func TestHTTPAPI_requireToken_missingHeader(t *testing.T) {
	d := httpTestDaemon()
	h := d.requireToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not be called when auth fails")
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing Authorization") {
		t.Fatalf("body = %q, want missing-auth message", rec.Body.String())
	}
}

func TestHTTPAPI_requireToken_wrongToken(t *testing.T) {
	d := httpTestDaemon()
	h := d.requireToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not be called on bad token")
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid bearer token") {
		t.Fatalf("body = %q, want invalid-token message", rec.Body.String())
	}
}

func TestHTTPAPI_requireToken_validToken(t *testing.T) {
	d := httpTestDaemon()
	called := false
	h := d.requireToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("inner handler was not called on valid token")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHTTPAPI_secureEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "abcd", false},
		{"", "", true},
		{"abc", "", false},
	}
	for _, c := range cases {
		got := secureEqual(c.a, c.b)
		if got != c.want {
			t.Errorf("secureEqual(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestHTTPAPI_parseTaskID(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"1", 1, false},
		{"42", 42, false},
		{"0", 0, true},
		{"abc", 0, true},
		{"", 0, true},
		{"-1", 0, true},
		{"1.5", 0, true},
	}
	for _, c := range cases {
		id, err := parseTaskID(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseTaskID(%q): expected error, got id=%d", c.in, id)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTaskID(%q): unexpected error %v", c.in, err)
			continue
		}
		if id != c.want {
			t.Errorf("parseTaskID(%q) = %d, want %d", c.in, id, c.want)
		}
	}
}

func TestHTTPAPI_writeJSON_okResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, Response{OK: true, Data: map[string]interface{}{"hello": "world"}})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	var got Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !got.OK {
		t.Errorf("got.OK = false, want true")
	}
}

func TestHTTPAPI_writeJSON_errorResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, Response{Error: "task not found"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for error response", rec.Code)
	}
}

func TestHTTPAPI_writeMethodNotAllowed_setsAllowHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMethodNotAllowed(rec, http.MethodGet, http.MethodPost)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	allow := rec.Header().Get("Allow")
	if allow != "GET, POST" {
		t.Errorf("Allow = %q, want %q", allow, "GET, POST")
	}
}

// TestHTTPAPI_post_requires_json_body checks that a POST to
// /v1/tasks with an empty body returns 400, not a panic.
func TestHTTPAPI_post_requires_json_body(t *testing.T) {
	d := httpTestDaemon()
	mux := http.NewServeMux()
	mux.Handle("/v1/tasks", d.requireToken("secret", http.HandlerFunc(d.httpTasks)))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/tasks", bytes.NewReader([]byte(`not-json`)))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
