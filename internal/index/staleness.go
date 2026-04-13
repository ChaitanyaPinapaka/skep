package index

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureFresh checks if the index is stale (HEAD moved since last index) and
// re-indexes only changed files. This is the on-demand alternative to a file watcher daemon.
// Returns (files re-indexed, error).
func EnsureFresh(root, skepDir string) (int, error) {
	dbPath := filepath.Join(skepDir, "index.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		return 0, fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	lastCommit := store.GetMeta("last_indexed_commit")
	currentCommit, err := gitHead(root)
	if err != nil {
		// No git — fall back to mtime-based staleness.
		// Walks all files but only reparses ones where mtime changed.
		indexer := NewIndexer(root, store)
		fc, _ := store.FileCount()
		if fc == 0 {
			if err := indexer.FullIndex(); err != nil {
				return 0, err
			}
			fc, _ = store.FileCount()
			return fc, nil
		}
		// IncrementalIndex compares content hashes, reparses changed files
		if err := indexer.IncrementalIndex(); err != nil {
			return 0, err
		}
		return 0, nil
	}

	if lastCommit == currentCommit {
		// Check for uncommitted changes via git status
		dirty := gitDirtyFiles(root)
		if len(dirty) == 0 {
			return 0, nil // fully up to date
		}
		// Re-index dirty files only
		indexer := NewIndexer(root, store)
		count := 0
		for _, f := range dirty {
			if err := indexer.IndexSingleFile(f); err == nil {
				count++
			}
		}
		return count, nil
	}

	if lastCommit == "" {
		// First time — full index
		indexer := NewIndexer(root, store)
		if err := indexer.FullIndex(); err != nil {
			return 0, err
		}
		store.SetMeta("last_indexed_commit", currentCommit)
		fc, _ := store.FileCount()
		return fc, nil
	}

	// Incremental: get changed files since last indexed commit
	changed, err := gitChangedFiles(root, lastCommit)
	if err != nil {
		// If we can't diff (e.g., commit was rebased away), fall back to full
		indexer := NewIndexer(root, store)
		if err := indexer.FullIndex(); err != nil {
			return 0, err
		}
		store.SetMeta("last_indexed_commit", currentCommit)
		fc, _ := store.FileCount()
		return fc, nil
	}

	indexer := NewIndexer(root, store)
	count := 0
	for _, f := range changed {
		if err := indexer.IndexSingleFile(f); err == nil {
			count++
		}
	}

	// Also handle deleted files
	indexer.RemoveDeletedFiles()

	store.SetMeta("last_indexed_commit", currentCommit)
	return count, nil
}

// FullIndex runs a complete index and records the commit hash.
func FullIndexAndRecord(root, skepDir string) (*Store, error) {
	dbPath := filepath.Join(skepDir, "index.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	indexer := NewIndexer(root, store)
	if err := indexer.FullIndex(); err != nil {
		store.Close()
		return nil, err
	}

	if commit, err := gitHead(root); err == nil {
		store.SetMeta("last_indexed_commit", commit)
	}

	return store, nil
}

func gitHead(root string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitChangedFiles(root, sinceCommit string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", sinceCommit, "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return splitNonEmpty(string(out)), nil
}

func gitDirtyFiles(root string) []string {
	cmd := exec.Command("git", "status", "--porcelain", "--no-renames")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range splitNonEmpty(string(out)) {
		// porcelain format: "XY filename"
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files
}

func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
