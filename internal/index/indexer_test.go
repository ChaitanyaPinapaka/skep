package index

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store, dir
}

func TestFullIndexGoSimple(t *testing.T) {
	store, _ := tempStore(t)
	defer store.Close()

	root := filepath.Join(testdataDir(), "go-simple")
	ix := NewIndexer(root, store)
	if err := ix.FullIndex(); err != nil {
		t.Fatalf("FullIndex: %v", err)
	}

	fc, _ := store.FileCount()
	if fc != 2 {
		t.Errorf("FileCount: got %d, want 2", fc)
	}

	sc, _ := store.SymbolCount()
	if sc == 0 {
		t.Error("SymbolCount: got 0, want >0")
	}
}

func TestFullIndexTSSimple(t *testing.T) {
	store, _ := tempStore(t)
	defer store.Close()

	root := filepath.Join(testdataDir(), "ts-simple")
	ix := NewIndexer(root, store)
	if err := ix.FullIndex(); err != nil {
		t.Fatalf("FullIndex: %v", err)
	}

	fc, _ := store.FileCount()
	if fc != 1 {
		t.Errorf("FileCount: got %d, want 1", fc)
	}

	sc, _ := store.SymbolCount()
	if sc == 0 {
		t.Error("SymbolCount: got 0, want >0")
	}
}

func TestFTS5Search(t *testing.T) {
	store, _ := tempStore(t)
	defer store.Close()

	root := filepath.Join(testdataDir(), "go-simple")
	ix := NewIndexer(root, store)
	ix.FullIndex()

	syms, err := store.SearchSymbols("Server", 10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(syms) == 0 {
		t.Error("no results for 'Server'")
	}

	found := false
	for _, s := range syms {
		if s.Name == "Server" || s.Name == "NewServer" {
			found = true
		}
	}
	if !found {
		t.Error("expected Server or NewServer in results")
	}
}

func TestFTS5SearchAuth(t *testing.T) {
	store, _ := tempStore(t)
	defer store.Close()

	root := filepath.Join(testdataDir(), "go-simple")
	ix := NewIndexer(root, store)
	ix.FullIndex()

	// FTS5 prefix search for partial matches
	syms, err := store.SearchSymbols("Auth*", 10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}

	found := false
	for _, s := range syms {
		if s.Name == "AuthMiddleware" {
			found = true
		}
	}
	if !found {
		t.Error("expected AuthMiddleware in 'Auth*' search results")
	}
}

func TestIncrementalIndex(t *testing.T) {
	store, _ := tempStore(t)
	defer store.Close()

	// Use a temp copy so we can modify files
	tmpRoot := t.TempDir()
	// Copy go-simple files
	srcDir := filepath.Join(testdataDir(), "go-simple")
	entries, _ := os.ReadDir(srcDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(srcDir, e.Name()))
		os.WriteFile(filepath.Join(tmpRoot, e.Name()), data, 0o644)
	}

	ix := NewIndexer(tmpRoot, store)
	ix.FullIndex()

	sc1, _ := store.SymbolCount()

	// Run incremental — nothing changed
	ix2 := NewIndexer(tmpRoot, store)
	ix2.IncrementalIndex()

	sc2, _ := store.SymbolCount()
	if sc1 != sc2 {
		t.Errorf("symbol count changed after no-op incremental: %d → %d", sc1, sc2)
	}

	// Add a new file
	os.WriteFile(filepath.Join(tmpRoot, "new.go"), []byte(`package main
func NewFunc() {}
`), 0o644)

	ix3 := NewIndexer(tmpRoot, store)
	ix3.IncrementalIndex()

	sc3, _ := store.SymbolCount()
	if sc3 <= sc2 {
		t.Errorf("symbol count should increase after adding file: %d → %d", sc2, sc3)
	}
}

func TestEdgesBuilt(t *testing.T) {
	store, _ := tempStore(t)
	defer store.Close()

	root := filepath.Join(testdataDir(), "go-simple")
	ix := NewIndexer(root, store)
	ix.FullIndex()

	edges, _ := store.AllEdges()
	// main() calls NewServer and Start — should produce edges
	if len(edges) == 0 {
		t.Error("no edges built — expected call edges from main()")
	}
}

func TestContentAddressableIDs(t *testing.T) {
	store, _ := tempStore(t)
	defer store.Close()

	root := filepath.Join(testdataDir(), "go-simple")
	ix := NewIndexer(root, store)
	ix.FullIndex()

	syms, _ := store.AllSymbols()
	for _, s := range syms {
		// IDs should be hex hashes, not filepath:name
		if len(s.ID) != 24 {
			t.Errorf("symbol %s has non-hash ID: %s (len %d)", s.Name, s.ID, len(s.ID))
		}
	}
}
