package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// HTTP API sidecar for the daemon. Same command surface as the
// Unix socket dispatcher (d.dispatch in daemon.go) — every HTTP
// handler in this file translates an HTTP request into the same
// daemon.Request struct and calls d.dispatch, so there is exactly
// one place in the codebase that knows how to execute a daemon
// command. Adding a new command means adding it once in dispatch
// and once in the route table here.
//
// Why HTTP at all when the Unix socket already works:
//   - Editor extensions (VS Code, JetBrains) speak HTTP natively
//     and don't want to reimplement a custom JSON-over-socket
//     protocol.
//   - Webhook ingestion (CI integrations, GitHub Actions) needs an
//     HTTP receiver.
//   - The HTTP API is gated by a per-daemon bearer token written to
//     .skep/api.token — same security level as a personal access
//     token. The Unix socket path stays peer-cred gated and is the
//     preferred path for local CLI usage.
//
// The listener binds to 127.0.0.1:0 so the OS picks a free port,
// the bound address is written to .skep/api.port for clients to
// discover, and shutdown is coordinated with the daemon's main
// context.

// startHTTPAPI brings up the HTTP listener bound to 127.0.0.1:0
// and writes the bound address to .skep/api.port. The returned
// http.Server is owned by the daemon and shut down via
// d.shutdownHTTPAPI in the daemon's defer chain.
//
// Errors here are fatal — if the HTTP listener cannot start, the
// daemon refuses to come up rather than running with a partial
// surface that some clients can reach and others cannot. The
// loopback bind to 127.0.0.1 is hardcoded; we never expose the
// HTTP API on a non-loopback interface from this code path.
func (d *Daemon) startHTTPAPI(token string) (*http.Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("http api listen: %w", err)
	}
	addr := listener.Addr().String()
	if err := WriteAPIAddr(d.SkepDir, addr); err != nil {
		listener.Close()
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/status", d.requireToken(token, http.HandlerFunc(d.httpStatus)))
	mux.Handle("/v1/tasks", d.requireToken(token, http.HandlerFunc(d.httpTasks)))
	// /v1/tasks/<id> handlers — net/http stdlib ServeMux uses
	// trailing slash to indicate a subtree match, so we register the
	// prefix here and split the path inside the handler.
	mux.Handle("/v1/tasks/", d.requireToken(token, http.HandlerFunc(d.httpTaskByID)))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// Bound writes too — a hung connection cannot tie up a goroutine forever.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		// Serve blocks until the listener closes; the daemon shuts
		// down the server explicitly via srv.Shutdown in the cleanup
		// path. Logging the error here would race with shutdown so
		// we let the daemon log it post-shutdown if needed.
		_ = srv.Serve(listener)
	}()
	return srv, nil
}

// shutdownHTTPAPI gracefully drains in-flight HTTP requests and
// removes the .skep/api.token + api.port files so a stale daemon
// state can't be picked up by the next start.
func (d *Daemon) shutdownHTTPAPI(srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	RemoveAPIToken(d.SkepDir)
}

// requireToken is the HTTP middleware that enforces the bearer
// token on every API request. Reads `Authorization: Bearer <token>`
// and rejects anything that doesn't match. Constant-time comparison
// to avoid leaking the token via response timing.
func (d *Daemon) requireToken(want string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, `{"error":"missing Authorization: Bearer <token> header"}`, http.StatusUnauthorized)
			return
		}
		got := strings.TrimPrefix(auth, "Bearer ")
		// Constant-time string comparison to avoid timing side
		// channels. crypto/subtle isn't available without an import,
		// but for fixed-length hex tokens a plain == is fine because
		// both sides have the same length and the comparison is
		// O(length) regardless. The real defense is the listener
		// being loopback-only.
		if !secureEqual(got, want) {
			http.Error(w, `{"error":"invalid bearer token"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// secureEqual is a constant-time string compare. Avoids importing
// crypto/subtle solely for this — both inputs are short hex strings
// and the loop bound is fixed at the longer of the two.
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// --- Handlers ---
//
// Each handler translates the HTTP request into a daemon.Request
// and calls d.dispatch. The dispatch function is the source of
// truth for every command — adding a new command means one new
// case there and one new route here.

// httpStatus: GET /v1/status
// Returns the same payload as the socket "status" command.
func (d *Daemon) httpStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resp := d.dispatch(Request{Cmd: "status"})
	writeJSON(w, resp)
}

// httpTasks: handles /v1/tasks (list, create)
//
//	GET  /v1/tasks                  → list_tasks
//	POST /v1/tasks  {description}   → create_task
func (d *Daemon) httpTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		resp := d.dispatch(Request{Cmd: "list_tasks"})
		writeJSON(w, resp)

	case http.MethodPost:
		var body struct {
			Description string `json:"description"`
			SourceRepo  string `json:"source_repo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
			return
		}
		if body.Description == "" {
			http.Error(w, `{"error":"description required"}`, http.StatusBadRequest)
			return
		}
		resp := d.dispatch(Request{
			Cmd:         "create_task",
			Description: body.Description,
			SourceRepo:  body.SourceRepo,
		})
		writeJSON(w, resp)

	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// httpTaskByID: handles /v1/tasks/<id>[/action]
//
//	GET  /v1/tasks/<id>             → get_task
//	POST /v1/tasks/<id>/approve     → approve_task
//	POST /v1/tasks/<id>/notify      → notify_task
func (d *Daemon) httpTaskByID(w http.ResponseWriter, r *http.Request) {
	// Strip the leading "/v1/tasks/" prefix and split the rest.
	rest := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"task id required"}`, http.StatusBadRequest)
		return
	}
	id, err := parseTaskID(parts[0])
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	// Dispatch on (method, action). Action is the optional second
	// path segment for state mutations (e.g. /tasks/3/approve).
	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		writeJSON(w, d.dispatch(Request{Cmd: "get_task", TaskID: id}))

	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "approve":
		writeJSON(w, d.dispatch(Request{Cmd: "approve_task", TaskID: id}))

	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "notify":
		writeJSON(w, d.dispatch(Request{Cmd: "notify_task", TaskID: id}))

	default:
		http.Error(w, `{"error":"unknown task action"}`, http.StatusNotFound)
	}
}

// parseTaskID converts a string segment to a positive int, with
// a clear error for the HTTP layer to surface to the caller.
func parseTaskID(s string) (int, error) {
	id := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("task id must be a positive integer, got %q", s)
		}
		id = id*10 + int(c-'0')
	}
	if id == 0 {
		return 0, fmt.Errorf("task id must be greater than zero")
	}
	return id, nil
}

// writeJSON serializes a daemon Response as JSON. Sets Content-Type
// and a 4xx code when the response carries an error string.
func writeJSON(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	if resp.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// writeMethodNotAllowed writes a 405 with the allowed methods in
// the response header per RFC 7231 §6.5.5.
func writeMethodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
}
