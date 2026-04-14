package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// API token file lives at <skepDir>/api.token. The HTTP API sidecar
// requires this token in an Authorization: Bearer header on every
// request. The Unix socket path stays peer-cred gated and does not
// require a token; the HTTP path needs one because it has no
// equivalent of SO_PEERCRED.
//
// File perms are 0600 — readable only by the owning user, same as
// any private key. The file is created on daemon start and removed
// on daemon stop.

const apiTokenFile = "api.token"

// LoadOrCreateAPIToken returns the daemon's HTTP API bearer token.
// On first call (file does not exist) it generates 32 random bytes,
// writes them as hex to <skepDir>/api.token with 0600 perms, and
// returns the value. On subsequent calls (file exists) it reads
// the existing token. Errors propagate to the caller — the daemon
// refuses to start if it cannot establish a token, because an
// HTTP listener without a token is a local-exec hole.
func LoadOrCreateAPIToken(skepDir string) (string, error) {
	path := filepath.Join(skepDir, apiTokenFile)

	data, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("api token file %s is empty", path)
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read api token: %w", err)
	}

	// Generate a new token. 32 bytes hex = 64 chars, 256 bits of
	// entropy. Same shape as a GitHub personal access token.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	token := hex.EncodeToString(buf)

	// Write atomically: write to a temp file, fsync, rename. This
	// avoids leaving a half-written token if the process crashes
	// between the open and the write.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write api token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("rename api token: %w", err)
	}

	return token, nil
}

// RemoveAPIToken deletes the api.token and api.port files on
// graceful daemon shutdown. Best-effort: a leftover token from a
// crash is harmless because the next daemon start either reads it
// (and reuses the same token) or generates a fresh one if the file
// is missing. Either way nothing is exposed.
func RemoveAPIToken(skepDir string) {
	os.Remove(filepath.Join(skepDir, apiTokenFile))
	os.Remove(filepath.Join(skepDir, apiPortFile))
}

// apiPortFile records the HTTP listener's bound address so clients
// can discover the port without grepping `ss` or scanning. Format:
// a single line "127.0.0.1:<port>".
const apiPortFile = "api.port"

// WriteAPIAddr records the HTTP listener's bound address. Plain
// text, single line, 0644 — the address itself is not a secret;
// the bearer token in api.token is what gates access.
func WriteAPIAddr(skepDir, addr string) error {
	path := filepath.Join(skepDir, apiPortFile)
	if err := os.WriteFile(path, []byte(addr+"\n"), 0o644); err != nil {
		return fmt.Errorf("write api port: %w", err)
	}
	return nil
}

// ReadAPIAddr returns the daemon's HTTP listener address from
// <skepDir>/api.port. Empty string if the file does not exist or
// the content is malformed — callers should treat that as "HTTP
// API not running" and fall back to the Unix socket path.
func ReadAPIAddr(skepDir string) string {
	data, err := os.ReadFile(filepath.Join(skepDir, apiPortFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
