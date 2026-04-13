package tasks

import (
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// TF-IDF cosine is the third dedup layer. Where trigrams catch
// morphological variation ("auth" ≈ "authentication") character-locally,
// TF-IDF cosine catches the token-level semantic case: two tasks that
// share the *rare* words carry more signal than two tasks that share
// the common ones.
//
//   "add rate limiting to /api/login"         and
//   "implement rate limits on login endpoint"
//
// share "rate", "limit", "login" — all rare across the corpus of
// active tasks, so their TF-IDF vectors point in nearly the same
// direction. Keyword overlap misses them (stemming + word boundaries);
// trigrams catch some of this but noisily.
//
// We compute IDF over the active-task corpus only — no global
// vocabulary, no training, no persisted state. Fresh every call.
// On a corpus of 50 active tasks this is ~10k float ops, which is
// faster than the subprocess fork of any LLM shell-out.

const defaultTFIDFThreshold = 0.60

func tfidfThreshold() float64 {
	v := strings.TrimSpace(os.Getenv("SKEP_DEDUP_TFIDF_THRESHOLD"))
	if v == "" {
		return defaultTFIDFThreshold
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f > 1 {
		return defaultTFIDFThreshold
	}
	return f
}

// tfidfVector maps token → TF-IDF weight for one document.
type tfidfVector map[string]float64

// buildTFIDFCorpus computes per-candidate TF-IDF vectors plus the
// vector for the new description, using IDF derived from the combined
// corpus (candidates + new doc). Returning both in one call means we
// only iterate the corpus once.
//
// Uses the "smoothed IDF" formula: idf(t) = ln((1+N) / (1+df(t))) + 1
// which is what scikit-learn uses by default — avoids division by zero
// when a token appears in every document, while still damping common
// tokens.
func buildTFIDFCorpus(candidates []*Task, newDesc string) (map[int]tfidfVector, tfidfVector) {
	// Step 1: tokenize everything once.
	candTokens := make(map[int][]string, len(candidates))
	for _, c := range candidates {
		candTokens[c.ID] = tokenize(c.Description)
	}
	newTokens := tokenize(newDesc)

	// Step 2: document frequency across the combined corpus.
	N := len(candidates) + 1
	df := make(map[string]int)
	seen := make(map[string]bool)
	for _, toks := range candTokens {
		for k := range seen {
			delete(seen, k)
		}
		for _, t := range toks {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	for k := range seen {
		delete(seen, k)
	}
	for _, t := range newTokens {
		if !seen[t] {
			seen[t] = true
			df[t]++
		}
	}

	idf := func(term string) float64 {
		return math.Log(float64(1+N)/float64(1+df[term])) + 1
	}

	// Step 3: build vectors. TF here is raw count / total tokens,
	// then multiplied by IDF and L2-normalized.
	vectorize := func(tokens []string) tfidfVector {
		if len(tokens) == 0 {
			return tfidfVector{}
		}
		tf := make(map[string]float64, len(tokens))
		for _, t := range tokens {
			tf[t]++
		}
		total := float64(len(tokens))
		vec := make(tfidfVector, len(tf))
		var norm float64
		for term, count := range tf {
			w := (count / total) * idf(term)
			vec[term] = w
			norm += w * w
		}
		if norm > 0 {
			norm = math.Sqrt(norm)
			for term := range vec {
				vec[term] /= norm
			}
		}
		return vec
	}

	candVecs := make(map[int]tfidfVector, len(candidates))
	for _, c := range candidates {
		candVecs[c.ID] = vectorize(candTokens[c.ID])
	}
	return candVecs, vectorize(newTokens)
}

// cosineSim returns the cosine similarity of two L2-normalized vectors.
// Both inputs are expected to be unit vectors — we skip the norm step
// because buildTFIDFCorpus already normalized them.
func cosineSim(a, b tfidfVector) float64 {
	// Iterate the smaller map for the dot product — irrelevant for
	// correctness, marginal for perf on sparse vectors.
	if len(a) > len(b) {
		a, b = b, a
	}
	var dot float64
	for k, va := range a {
		if vb, ok := b[k]; ok {
			dot += va * vb
		}
	}
	return dot
}

// tfidfBatch builds per-candidate TF-IDF vectors and returns the
// cosine similarity of each candidate against the new description.
// Used by runScoringLayer as the corpus-aware scoring path — TF-IDF
// can't be computed pairwise because IDF needs the full corpus.
func tfidfBatch(candidates []*Task, newDesc string) map[int]float64 {
	candVecs, newVec := buildTFIDFCorpus(candidates, newDesc)
	if len(newVec) == 0 {
		return nil
	}
	scores := make(map[int]float64, len(candidates))
	for _, c := range candidates {
		scores[c.ID] = cosineSim(candVecs[c.ID], newVec)
	}
	return scores
}

// CheckDedupTFIDF is the TF-IDF cosine dedup layer. A thin wrapper
// over runScoringLayer with the corpus-aware Batch path.
func CheckDedupTFIDF(store *index.Store, description, repoRoot string) (*DedupResult, error) {
	return runScoringLayer(store, description, repoRoot, layerSpec{
		Name:        "tfidf",
		Reason:      "tf-idf duplicate",
		Threshold:   tfidfThreshold(),
		Batch:       tfidfBatch,
		ScoreFormat: "cos=%.2f",
	})
}
