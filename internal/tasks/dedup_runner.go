package tasks

import (
	"fmt"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// layerSpec describes one pass of the dedup pipeline in enough
// structural detail that runScoringLayer can drive it without the
// three scoring layers duplicating their boilerplate.
//
// The old implementation had each of Trigram / TFIDF / MinHash carry
// its own copy of: load candidates → loop → score → track best →
// threshold check → log outcome → return. All three loops were the
// same; the only differences were the scoring function, the layer
// name, and the human-readable reason string. This spec captures
// exactly those three differences and nothing else, so adding a
// fifth layer in the future is a 10-line addition rather than a
// 60-line copy-paste.
type layerSpec struct {
	// Name is the log/label identifier: "trigram", "tfidf", "minhash".
	Name string
	// Reason is the human-readable noun the reason string leads with,
	// e.g. "near-duplicate", "tf-idf duplicate", "minhash duplicate".
	Reason string
	// Threshold is the minimum similarity required to flag a hit.
	Threshold float64
	// Score computes pairwise similarity between two task descriptions.
	// Used by layers that don't need corpus-wide state.
	Score func(a, b string) float64
	// Batch is the corpus-aware alternative to Score: given all active
	// candidates and the new description, return a map of task id →
	// similarity. Used by TF-IDF, which needs IDF over the full corpus.
	// When Batch is set it takes precedence over Score.
	Batch func(candidates []*Task, newDesc string) map[int]float64
	// ScoreFormat controls how the similarity value is rendered in the
	// reason string. Default "%.2f" if empty. TF-IDF uses "cos=%.2f",
	// minhash uses "jaccard≈%.2f", etc.
	ScoreFormat string
}

// runScoringLayer is the shared body that drives one similarity pass
// over the active-task set. Handles empty-candidate shortcut, best-
// score tracking, threshold comparison, log emission, and result
// construction so each layer's public entry point can be a 5-line
// wrapper that just fills in the spec fields.
//
// Calls the spec's Batch function when set (TF-IDF path), falling
// back to pairwise Score otherwise. Never allocates unless there's
// at least one candidate — the caller's hot path for "fresh repo,
// zero tasks" remains free.
func runScoringLayer(store *index.Store, description, repoRoot string, spec layerSpec) (*DedupResult, error) {
	start := time.Now()
	candidates := activeTaskCandidates(store)
	if len(candidates) == 0 {
		LogDedup(repoRoot, DedupLogEntry{
			Layer:    spec.Name,
			Hit:      false,
			TaskDesc: description,
			Duration: time.Since(start),
			Reason:   "no candidates",
		})
		return &DedupResult{IsDuplicate: false, Layer: spec.Name}, nil
	}

	var (
		bestID     int
		bestScore  float64
		bestStatus string
	)
	if spec.Batch != nil {
		// Corpus-aware path (TF-IDF). The batch function handles its
		// own tokenization and per-document vector math in one pass.
		scores := spec.Batch(candidates, description)
		for _, c := range candidates {
			if s := scores[c.ID]; s > bestScore {
				bestID, bestScore, bestStatus = c.ID, s, c.Status
			}
		}
	} else {
		// Pairwise path (trigram, minhash, any future symmetric metric).
		for _, c := range candidates {
			s := spec.Score(c.Description, description)
			if s > bestScore {
				bestID, bestScore, bestStatus = c.ID, s, c.Status
			}
		}
	}

	format := spec.ScoreFormat
	if format == "" {
		format = "%.2f"
	}

	if bestScore < spec.Threshold {
		LogDedup(repoRoot, DedupLogEntry{
			Layer:       spec.Name,
			Hit:         false,
			Score:       bestScore,
			TaskDesc:    description,
			CandidateID: bestID,
			Duration:    time.Since(start),
		})
		return &DedupResult{IsDuplicate: false, Layer: spec.Name, Score: bestScore}, nil
	}

	scoreText := fmt.Sprintf(format, bestScore)
	reason := fmt.Sprintf("%s of task #%d (%s %s)", spec.Reason, bestID, spec.Name, scoreText)
	if bestStatus == StatusExecuting {
		reason = fmt.Sprintf("%s in progress as task #%d (%s %s)", spec.Reason, bestID, spec.Name, scoreText)
	}
	LogDedup(repoRoot, DedupLogEntry{
		Layer:       spec.Name,
		Hit:         true,
		Score:       bestScore,
		TaskDesc:    description,
		CandidateID: bestID,
		Duration:    time.Since(start),
		Reason:      reason,
	})
	return &DedupResult{
		IsDuplicate: true,
		Reason:      reason,
		TaskID:      bestID,
		Layer:       spec.Name,
		Score:       bestScore,
	}, nil
}
