package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Request is the daemon socket protocol request.
type Request struct {
	Cmd         string `json:"cmd"` // task, approve, status, stop, create_task
	TaskID      int    `json:"task_id,omitempty"`
	Description string `json:"description,omitempty"`
	SourceRepo  string `json:"source_repo,omitempty"`
}

// Response is the daemon socket protocol response.
type Response struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Data  interface{} `json:"data,omitempty"`
}

// Listen creates a Unix-socket listener for the daemon.
//
// Attempts, in order:
//  1. `<skepDir>/agent.sock` — native Linux/macOS, keeps the socket co-located
//     with the rest of skep's state.
//  2. `/tmp/skep-<hash>/agent.sock` — fallback for WSL2 Windows-drive paths
//     (`/mnt/c/...`) where the native UDS namespace can't be used and for
//     paths that exceed the Linux sun_path cap (~108 bytes).
//
// There is deliberately NO TCP fallback. A loopback TCP listener with no
// peer authentication would let any local process (other user on the box,
// a container on the host network, a browser calling fetch() to
// 127.0.0.1) post `create_task` / `approve_task` and cause execution of
// test_cmd. See the security audit notes in docs/future-scope.md for
// the history.
//
// If both Unix-socket attempts fail, the daemon refuses to start and
// surfaces both errors so the user can see why (usually: skepDir path
// is too long, or /tmp is read-only).
func Listen(skepDir, repoRoot string) (net.Listener, error) {
	// Try 1: socket inside .skep/ (native Linux, macOS)
	sockPath := filepath.Join(skepDir, "agent.sock")
	os.Remove(sockPath)
	ln, err1 := net.Listen("unix", sockPath)
	if err1 == nil {
		os.WriteFile(filepath.Join(skepDir, "agent.sock.path"), []byte(sockPath), 0o644)
		return ln, nil
	}

	// Try 2: socket in /tmp/ (WSL2 with /mnt/c/ paths, or long repo paths)
	sockDir := socketDir(repoRoot)
	os.MkdirAll(sockDir, 0o700)
	sockPath = filepath.Join(sockDir, "agent.sock")
	os.Remove(sockPath)
	ln, err2 := net.Listen("unix", sockPath)
	if err2 == nil {
		os.WriteFile(filepath.Join(skepDir, "agent.sock.path"), []byte(sockPath), 0o644)
		return ln, nil
	}

	return nil, fmt.Errorf(
		"skep daemon: unable to create a Unix domain socket for IPC.\n"+
			"  Tried: %s → %v\n"+
			"  Tried: %s → %v\n"+
			"This usually means the absolute socket path is longer than the\n"+
			"kernel's sun_path limit (~108 bytes) or /tmp is read-only.\n"+
			"Move the repo to a shorter path or ensure /tmp is writable.\n"+
			"skep does not fall back to TCP — a loopback listener without\n"+
			"peer authentication is a local-exec risk.",
		filepath.Join(skepDir, "agent.sock"), err1,
		sockPath, err2,
	)
}

// Dial connects to the daemon. Reads the socket path from
// .skep/agent.sock.path. There is no TCP fallback — see Listen().
func Dial(skepDir string) (net.Conn, error) {
	sockPathData, err := os.ReadFile(filepath.Join(skepDir, "agent.sock.path"))
	if err != nil {
		return nil, fmt.Errorf("daemon not running")
	}
	sockPath := strings.TrimSpace(string(sockPathData))
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon not running")
	}
	return conn, nil
}

// Send sends a request to the daemon and returns the response.
func Send(skepDir string, req Request) (*Response, error) {
	conn, err := Dial(skepDir)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("recv: %w", err)
	}
	return &resp, nil
}

// IsRunning checks if the daemon is reachable.
func IsRunning(skepDir string) bool {
	resp, err := Send(skepDir, Request{Cmd: "status"})
	return err == nil && resp.OK
}

// CleanupListener removes socket files and the sock-path marker.
// The old TCP daemon.port file is also removed if an older daemon
// left one behind, so upgrades don't strand stale state.
func CleanupListener(skepDir, repoRoot string) {
	sockDir := socketDir(repoRoot)
	os.Remove(filepath.Join(sockDir, "agent.sock"))
	os.Remove(sockDir)
	os.Remove(filepath.Join(skepDir, "agent.sock"))
	os.Remove(filepath.Join(skepDir, "agent.sock.path"))
	os.Remove(filepath.Join(skepDir, "daemon.port")) // legacy, pre-S1
}

func socketDir(repoRoot string) string {
	h := sha256.Sum256([]byte(repoRoot))
	return filepath.Join(os.TempDir(), fmt.Sprintf("skep-%x", h[:8]))
}
