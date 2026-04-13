package index

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/ChaitanyaPinapaka/skep/internal/parser"
)

// pendingEdge stores a reference to resolve after all symbols are inserted.
type pendingEdge struct {
	callerID string
	refName  string
}

// parsedFile holds the result of parsing a single file (done in parallel).
type parsedFile struct {
	wf      WalkResult
	hash    []byte
	symbols []parser.ExtractedSymbol
}

// Indexer manages full and incremental indexing of a repo.
type Indexer struct {
	Root         string
	Store        *Store
	pendingEdges []pendingEdge
}

// NewIndexer creates a new indexer for the given repo root.
func NewIndexer(root string, store *Store) *Indexer {
	return &Indexer{Root: root, Store: store}
}

// FullIndex walks the entire repo and indexes all source files.
// Parsing + hashing is parallelized across NumCPU workers.
// All DB writes are serialized in a single transaction.
func (ix *Indexer) FullIndex() error {
	prog := NewProgress()

	prog.Phase("scanning files...")
	files, err := WalkRepo(ix.Root)
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	prog.Phase("parsing %d files...", len(files))
	parsed := ix.parseParallel(files)

	prog.Phase("writing to index...")
	ix.Store.DB().Exec("BEGIN")
	defer ix.Store.DB().Exec("ROLLBACK")

	for _, pf := range parsed {
		ix.writeFile(pf)
	}

	prog.Phase("building edges...")
	ix.BuildEdges()

	_, err = ix.Store.DB().Exec("COMMIT")
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	prog.Phase("done")
	prog.Done()
	return nil
}

// parseParallel fans out file parsing + hashing to NumCPU workers.
func (ix *Indexer) parseParallel(files []WalkResult) []parsedFile {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8 // cap to avoid too many tree-sitter instances
	}
	if len(files) < workers {
		workers = len(files)
	}

	in := make(chan WalkResult, len(files))
	out := make(chan parsedFile, len(files))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for wf := range in {
				hash, err := HashFile(wf.AbsPath)
				if err != nil {
					continue
				}

				// Parser errors on a single file are intentionally
				// swallowed: we'd rather index the other 99% of the
				// repo than fail the whole job on one bad parse. Files
				// with parse errors land in the index with zero
				// symbols and can be queried by path.
				var symbols []parser.ExtractedSymbol
				if parser.Supported(wf.Language) {
					symbols, _ = parser.ParseFile(wf.AbsPath, wf.Language)
				}

				out <- parsedFile{wf: wf, hash: hash, symbols: symbols}
			}
		}()
	}

	// Feed files to workers
	for _, wf := range files {
		in <- wf
	}
	close(in)

	// Wait for all workers, then close output
	go func() {
		wg.Wait()
		close(out)
	}()

	// Collect results
	var results []parsedFile
	for pf := range out {
		results = append(results, pf)
	}
	return results
}

// writeFile writes a parsed file's data to the store (must be called in serial).
func (ix *Indexer) writeFile(pf parsedFile) {
	ix.Store.DeleteSymbolsByFile(pf.wf.RelPath)

	f := &File{
		Path:         pf.wf.RelPath,
		ContentHash:  pf.hash,
		Language:     pf.wf.Language,
		SizeBytes:    pf.wf.Size,
		LastModified: pf.wf.ModTime,
		SymbolCount:  len(pf.symbols),
	}
	ix.Store.UpsertFile(f)

	for _, sym := range pf.symbols {
		id := symbolID(pf.wf.RelPath, sym.Name, sym.Kind, sym.Signature)
		parentID := ""
		if sym.ParentName != "" {
			parentID = symbolID(pf.wf.RelPath, sym.ParentName, "", "")
		}
		s := &Symbol{
			ID:         id,
			Name:       sym.Name,
			Kind:       sym.Kind,
			FilePath:   pf.wf.RelPath,
			Line:       sym.Line,
			EndLine:    sym.EndLine,
			Signature:  sym.Signature,
			DocComment: sym.DocComment,
			ParentID:   parentID,
		}
		ix.Store.InsertSymbol(s)

		for _, ref := range sym.References {
			ix.pendingEdges = append(ix.pendingEdges, pendingEdge{callerID: id, refName: ref})
		}
	}
}

// IncrementalIndex re-indexes only changed files.
// Loads all indexed files into a map once (1 query) instead of per-file queries.
func (ix *Indexer) IncrementalIndex() error {
	files, err := WalkRepo(ix.Root)
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	// Load all existing files into map — one query, not N queries
	allExisting, err := ix.Store.AllFiles()
	if err != nil {
		return fmt.Errorf("load files: %w", err)
	}
	existingMap := make(map[string]*File, len(allExisting))
	for _, f := range allExisting {
		existingMap[f.Path] = f
	}

	currentPaths := make(map[string]bool)

	for _, wf := range files {
		currentPaths[wf.RelPath] = true

		existing := existingMap[wf.RelPath]
		if existing != nil && existing.LastModified == wf.ModTime {
			continue
		}

		hash, err := HashFile(wf.AbsPath)
		if err != nil {
			continue
		}

		if existing != nil && bytes.Equal(existing.ContentHash, hash) {
			continue
		}

		ix.indexFileWithHash(wf, hash)
	}

	// Remove deleted files — already have allExisting, no second query needed
	for _, f := range allExisting {
		if !currentPaths[f.Path] {
			ix.Store.DeleteSymbolsByFile(f.Path)
			ix.Store.DeleteFile(f.Path)
		}
	}

	return nil
}

// IndexSingleFile re-indexes a single file by relative path.
func (ix *Indexer) IndexSingleFile(relPath string) error {
	absPath := filepath.Join(ix.Root, relPath)
	lang := detectLanguage(absPath)
	if lang == "" {
		return nil
	}

	hash, err := HashFile(absPath)
	if err != nil {
		return fmt.Errorf("hash %s: %w", relPath, err)
	}

	existing, err := ix.Store.GetFile(relPath)
	if err != nil {
		return err
	}
	if existing != nil && bytes.Equal(existing.ContentHash, hash) {
		return nil
	}

	wf := WalkResult{
		AbsPath:  absPath,
		RelPath:  relPath,
		Language: lang,
	}
	return ix.indexFileWithHash(wf, hash)
}

// RemoveDeletedFiles removes files from the index that no longer exist on disk.
func (ix *Indexer) RemoveDeletedFiles() {
	allFiles, err := ix.Store.AllFiles()
	if err != nil {
		return
	}
	currentPaths := make(map[string]bool)
	files, _ := WalkRepo(ix.Root)
	for _, f := range files {
		currentPaths[f.RelPath] = true
	}
	for _, f := range allFiles {
		if !currentPaths[f.Path] {
			ix.Store.DeleteSymbolsByFile(f.Path)
			ix.Store.DeleteFile(f.Path)
		}
	}
}

func (ix *Indexer) indexFileWithHash(wf WalkResult, hash []byte) error {
	if err := ix.Store.DeleteSymbolsByFile(wf.RelPath); err != nil {
		return fmt.Errorf("delete old symbols: %w", err)
	}

	var symbols []parser.ExtractedSymbol
	if parser.Supported(wf.Language) {
		symbols, _ = parser.ParseFile(wf.AbsPath, wf.Language)
	}

	f := &File{
		Path:         wf.RelPath,
		ContentHash:  hash,
		Language:     wf.Language,
		SizeBytes:    wf.Size,
		LastModified: wf.ModTime,
		SymbolCount:  len(symbols),
	}
	if err := ix.Store.UpsertFile(f); err != nil {
		return fmt.Errorf("upsert file: %w", err)
	}

	for _, sym := range symbols {
		id := symbolID(wf.RelPath, sym.Name, sym.Kind, sym.Signature)
		parentID := ""
		if sym.ParentName != "" {
			parentID = symbolID(wf.RelPath, sym.ParentName, "", "")
		}
		s := &Symbol{
			ID:         id,
			Name:       sym.Name,
			Kind:       sym.Kind,
			FilePath:   wf.RelPath,
			Line:       sym.Line,
			EndLine:    sym.EndLine,
			Signature:  sym.Signature,
			DocComment: sym.DocComment,
			ParentID:   parentID,
		}
		ix.Store.InsertSymbol(s)
	}

	return nil
}

// BuildEdges resolves all pending references into edges. Call after FullIndex.
// Uses a SQL query for name→ID lookup instead of loading all symbols into memory.
func (ix *Indexer) BuildEdges() {
	if len(ix.pendingEdges) == 0 {
		return
	}

	// Build name→ID map from a single indexed query (not AllSymbols)
	nameToID := make(map[string]string)
	rows, err := ix.Store.DB().Query("SELECT name, id FROM symbols")
	if err != nil {
		return
	}
	for rows.Next() {
		var name, id string
		rows.Scan(&name, &id)
		nameToID[name] = id
	}
	rows.Close()

	for _, pe := range ix.pendingEdges {
		targetID, ok := nameToID[pe.refName]
		if !ok || targetID == pe.callerID {
			continue
		}
		ix.Store.InsertEdge(&Edge{FromID: pe.callerID, ToID: targetID, Kind: "call"})
	}

	ix.pendingEdges = nil
}

func symbolID(filePath, name, kind, signature string) string {
	h := sha256.Sum256([]byte(filePath + "\x00" + name + "\x00" + kind + "\x00" + signature))
	return fmt.Sprintf("%x", h[:12])
}
