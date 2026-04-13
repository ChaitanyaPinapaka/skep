package graph

import (
	"fmt"
	"math"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

const (
	dampingFactor = 0.85
	iterations    = 20
	convergence   = 1e-6
)

// ComputePageRank runs PageRank over the symbol call graph and stores results.
// Phase 4: used for ranking symbols in MCP tool responses.
func ComputePageRank(store *index.Store) error {
	syms, err := store.AllSymbols()
	if err != nil {
		return fmt.Errorf("load symbols: %w", err)
	}
	edges, err := store.AllEdges()
	if err != nil {
		return fmt.Errorf("load edges: %w", err)
	}

	if len(syms) == 0 {
		return nil
	}

	n := float64(len(syms))
	rank := make(map[string]float64)
	for _, s := range syms {
		rank[s.ID] = 1.0 / n
	}

	outgoing := make(map[string][]string)
	for _, e := range edges {
		outgoing[e.FromID] = append(outgoing[e.FromID], e.ToID)
	}

	for iter := 0; iter < iterations; iter++ {
		newRank := make(map[string]float64)

		// Accumulate dangling node mass in O(V), not O(V^2)
		danglingMass := 0.0
		for _, s := range syms {
			if len(outgoing[s.ID]) == 0 {
				danglingMass += rank[s.ID]
			}
		}
		danglingShare := dampingFactor * danglingMass / n

		for _, s := range syms {
			newRank[s.ID] = (1.0-dampingFactor)/n + danglingShare
		}

		for _, s := range syms {
			outs := outgoing[s.ID]
			if len(outs) > 0 {
				share := rank[s.ID] * dampingFactor / float64(len(outs))
				for _, target := range outs {
					newRank[target] += share
				}
			}
		}

		diff := 0.0
		for _, s := range syms {
			diff += math.Abs(newRank[s.ID] - rank[s.ID])
		}
		rank = newRank
		if diff < convergence {
			break
		}
	}

	// Batch all rank updates in a single transaction
	store.DB().Exec("BEGIN")
	defer store.DB().Exec("ROLLBACK")
	for id, r := range rank {
		if err := store.UpdateSymbolRank(id, r); err != nil {
			return fmt.Errorf("update rank %s: %w", id, err)
		}
	}
	store.DB().Exec("COMMIT")

	return nil
}
