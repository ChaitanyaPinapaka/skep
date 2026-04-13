package tasks

import (
	"os"
	"strconv"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// Trigram Jaccard is the second dedup layer. It catches near-duplicates
// that the keyword-overlap layer misses on short strings:
//
//	"add auth endpoint"         ≈ "add authentication endpoint"
//	"fix login bug"             ≈ "fix login issue"
//	"refactor UserService.save" ≈ "refactor User Service save method"
//
// The keyword layer measures word-set overlap; it punishes morphological
// variation ("auth" vs "authentication" share zero tokens >2 chars).
// Character n-grams collapse those differences because the substring
// "auth" appears in both.
//
// We use 3-grams as the sweet spot: 2-grams are too permissive (every
// English string looks similar), 4-grams too strict for short task
// descriptions.

const defaultTrigramThreshold = 0.55

// trigramThreshold reads SKEP_DEDUP_TRIGRAM_THRESHOLD (clamped to (0,1])
// or returns the default. Trigrams are noisier than exact keyword
// matching, so the default is lower than the keyword threshold — a
// trigram hit is "probably similar" not "definitely the same."
func trigramThreshold() float64 {
	v := strings.TrimSpace(os.Getenv("SKEP_DEDUP_TRIGRAM_THRESHOLD"))
	if v == "" {
		return defaultTrigramThreshold
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f > 1 {
		return defaultTrigramThreshold
	}
	return f
}

// charNgrams returns the set of lowercase character n-grams in s with
// non-alphanumeric chars collapsed to a single space. Padding with a
// leading/trailing space makes the start and end of the string count
// as distinct trigrams, which improves discrimination on short strings.
func charNgrams(s string, n int) map[string]struct{} {
	normalized := make([]byte, 0, len(s)+2)
	normalized = append(normalized, ' ')
	prevSpace := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			normalized = append(normalized, c)
			prevSpace = false
			continue
		}
		if !prevSpace {
			normalized = append(normalized, ' ')
			prevSpace = true
		}
	}
	if !prevSpace {
		normalized = append(normalized, ' ')
	}

	set := make(map[string]struct{}, len(normalized))
	if len(normalized) < n {
		return set
	}
	for i := 0; i <= len(normalized)-n; i++ {
		set[string(normalized[i:i+n])] = struct{}{}
	}
	return set
}

// trigramJaccard returns |A ∩ B| / |A ∪ B| over the character-trigram
// sets of a and b. Symmetric, unlike keywordOverlap.
func trigramJaccard(a, b string) float64 {
	setA := charNgrams(a, 3)
	setB := charNgrams(b, 3)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	intersect := 0
	for k := range setA {
		if _, ok := setB[k]; ok {
			intersect++
		}
	}
	union := len(setA) + len(setB) - intersect
	if union == 0 {
		return 0
	}
	return float64(intersect) / float64(union)
}

// CheckDedupTrigram is the trigram-Jaccard dedup layer. A thin wrapper
// over runScoringLayer that supplies the name, reason text, and
// scoring function — the shared driver handles candidate loading,
// best-match tracking, logging, and result construction.
func CheckDedupTrigram(store *index.Store, description, repoRoot string) (*DedupResult, error) {
	return runScoringLayer(store, description, repoRoot, layerSpec{
		Name:        "trigram",
		Reason:      "near-duplicate",
		Threshold:   trigramThreshold(),
		Score:       trigramJaccard,
		ScoreFormat: "jaccard=%.2f",
	})
}
