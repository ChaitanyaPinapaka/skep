package index

// IndexDiff represents changes between two index snapshots.
type IndexDiff struct {
	AddedSymbols   []*Symbol
	RemovedSymbols []*Symbol
	ChangedSymbols []SymbolChange
}

// SymbolChange represents a modified symbol.
type SymbolChange struct {
	Before *Symbol
	After  *Symbol
}

// SnapshotSymbols takes a snapshot of current symbols for later diffing.
func SnapshotSymbols(store *Store) (map[string]*Symbol, error) {
	syms, err := store.AllSymbols()
	if err != nil {
		return nil, err
	}
	m := make(map[string]*Symbol, len(syms))
	for _, s := range syms {
		m[s.ID] = s
	}
	return m, nil
}

// DiffSnapshots computes what changed between before and after.
func DiffSnapshots(before, after map[string]*Symbol) *IndexDiff {
	diff := &IndexDiff{}

	for id, s := range after {
		old, exists := before[id]
		if !exists {
			diff.AddedSymbols = append(diff.AddedSymbols, s)
		} else if old.Signature != s.Signature || old.Kind != s.Kind {
			diff.ChangedSymbols = append(diff.ChangedSymbols, SymbolChange{Before: old, After: s})
		}
	}

	for id, s := range before {
		if _, exists := after[id]; !exists {
			diff.RemovedSymbols = append(diff.RemovedSymbols, s)
		}
	}

	return diff
}

// IsEmpty returns true if no changes were detected.
func (d *IndexDiff) IsEmpty() bool {
	return len(d.AddedSymbols) == 0 && len(d.RemovedSymbols) == 0 && len(d.ChangedSymbols) == 0
}
