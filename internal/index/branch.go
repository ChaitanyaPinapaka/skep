package index

import (
	"fmt"
	"os"
	"path/filepath"
)

// BranchDBPath returns the path for a branch-specific index database.
func BranchDBPath(skepDir, branch string) string {
	return filepath.Join(skepDir, "branches", branch+".db")
}

// IndexBranch creates a full index of the repo at its current state into a branch-specific DB.
// The main index.db is never touched.
func IndexBranch(repoRoot, skepDir, branch string) (*Store, error) {
	branchDir := filepath.Join(skepDir, "branches")
	if err := os.MkdirAll(branchDir, 0o755); err != nil {
		return nil, fmt.Errorf("create branches dir: %w", err)
	}

	dbPath := BranchDBPath(skepDir, branch)

	// Remove old branch DB if it exists
	os.Remove(dbPath)

	store, err := OpenStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open branch store: %w", err)
	}

	indexer := NewIndexer(repoRoot, store)
	if err := indexer.FullIndex(); err != nil {
		store.Close()
		return nil, fmt.Errorf("index branch: %w", err)
	}

	return store, nil
}

// DiffBranch compares symbols in the branch DB against the main DB and returns the diff.
// mainStore is the main index.db, branchStore is the branch-specific DB.
func DiffBranch(mainStore, branchStore *Store) (*IndexDiff, error) {
	mainSyms, err := SnapshotSymbols(mainStore)
	if err != nil {
		return nil, fmt.Errorf("snapshot main symbols: %w", err)
	}

	branchSyms, err := SnapshotSymbols(branchStore)
	if err != nil {
		return nil, fmt.Errorf("snapshot branch symbols: %w", err)
	}

	return DiffSnapshots(mainSyms, branchSyms), nil
}

// CleanupBranchDB removes a branch-specific index database.
func CleanupBranchDB(skepDir, branch string) {
	os.Remove(BranchDBPath(skepDir, branch))
}
