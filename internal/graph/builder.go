package graph

import (
	"fmt"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// BuildEdgesFromRefs creates call edges from extracted references during indexing.
// Phase 3: wired during cross-repo contract diffing.
func BuildEdgesFromRefs(store *index.Store, callerID string, refs []string) error {
	syms, err := store.AllSymbols()
	if err != nil {
		return fmt.Errorf("load symbols: %w", err)
	}

	nameToID := make(map[string]string)
	for _, s := range syms {
		nameToID[s.Name] = s.ID
	}

	for _, ref := range refs {
		targetID, ok := nameToID[ref]
		if !ok || targetID == callerID {
			continue
		}
		if err := store.InsertEdge(&index.Edge{
			FromID: callerID,
			ToID:   targetID,
			Kind:   "call",
		}); err != nil {
			return fmt.Errorf("insert edge %s->%s: %w", callerID, targetID, err)
		}
	}
	return nil
}
