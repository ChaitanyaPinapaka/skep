package tasks

import (
	"hash/fnv"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
)

// MinHash is the fourth and last pre-LLM dedup layer. It catches the
// paraphrase case the previous three layers miss:
//
//   "Make the homepage load faster by caching the hero image"   vs
//   "Cache the landing page banner to speed up first paint"
//
// Different tokens, different character runs, different rare-word
// overlap — but the same intent.
//
// MinHash approximates Jaccard similarity over *shingles* (n-grams of
// words) via hashed random permutations. On short strings the approx
// error is high, so this layer is the "last resort before LLM" rather
// than a primary signal: we only flag when the MinHash estimate is
// very confident.
//
// Implementation notes:
//   - Shingles are 2-word grams (bigrams). Single-word grams collapse
//     to the keyword layer; 3+ word grams are too sparse on 5-8 word
//     task descriptions.
//   - numHashes = 64. Enough precision for ±0.06 error at 95%
//     confidence (error ≈ 1/√k), cheap enough to compute on the fly.
//   - Random permutations are simulated with a + b*h mod p over a
//     fixed 64-hash seed table. Deterministic across runs so two
//     skep processes would compute the same signature for the same
//     input.

const defaultMinHashThreshold = 0.70
const minHashNumHashes = 64

func minHashThreshold() float64 {
	v := strings.TrimSpace(os.Getenv("SKEP_DEDUP_MINHASH_THRESHOLD"))
	if v == "" {
		return defaultMinHashThreshold
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f > 1 {
		return defaultMinHashThreshold
	}
	return f
}

// minHashSeeds holds the (a, b) pairs for each of the 64 hash
// permutations. Picked at package init so we don't re-derive them on
// every call — the values themselves are deterministic.
var minHashSeeds [minHashNumHashes][2]uint64

func init() {
	// Use a linear congruential walk to fill the seed table. Values
	// don't need to be cryptographic — we just want 64 distinct (a,b)
	// pairs with decent bit spread.
	var state uint64 = 0x9E3779B97F4A7C15
	for i := 0; i < minHashNumHashes; i++ {
		state = state*6364136223846793005 + 1442695040888963407
		a := state | 1 // must be odd so a*h spans the full ring
		state = state*6364136223846793005 + 1442695040888963407
		b := state
		minHashSeeds[i] = [2]uint64{a, b}
	}
}

// wordShingles returns the set of 2-word grams of the tokenized form
// of s. Duplicated shingles collapse to a single set entry.
func wordShingles(s string) map[string]struct{} {
	tokens := tokenize(s)
	set := make(map[string]struct{}, len(tokens))
	if len(tokens) < 2 {
		// Single-token docs get the token itself as a shingle so
		// MinHash can still compare them meaningfully.
		for _, t := range tokens {
			set[t] = struct{}{}
		}
		return set
	}
	for i := 0; i < len(tokens)-1; i++ {
		set[tokens[i]+" "+tokens[i+1]] = struct{}{}
	}
	return set
}

// minHashSignature computes a 64-dim signature over a shingle set.
// Each component is the min over all shingles of (a*h + b) mod p,
// where h is the FNV-64 hash of the shingle and (a, b) comes from
// minHashSeeds.
func minHashSignature(shingles map[string]struct{}) [minHashNumHashes]uint64 {
	var sig [minHashNumHashes]uint64
	for i := range sig {
		sig[i] = math.MaxUint64
	}
	if len(shingles) == 0 {
		return sig
	}
	hasher := fnv.New64a()
	for sh := range shingles {
		hasher.Reset()
		hasher.Write([]byte(sh))
		h := hasher.Sum64()
		for i := 0; i < minHashNumHashes; i++ {
			perm := minHashSeeds[i][0]*h + minHashSeeds[i][1]
			if perm < sig[i] {
				sig[i] = perm
			}
		}
	}
	return sig
}

// minHashSimilarity estimates Jaccard(A, B) as the fraction of
// matching components between two signatures.
func minHashSimilarity(a, b [minHashNumHashes]uint64) float64 {
	matches := 0
	for i := 0; i < minHashNumHashes; i++ {
		if a[i] == b[i] {
			matches++
		}
	}
	return float64(matches) / float64(minHashNumHashes)
}

// minHashScore is the pairwise wrapper runScoringLayer calls for the
// MinHash layer. Signatures are computed per pair — cheaper than
// caching for a single-run scan, and keeps the layer stateless.
func minHashScore(a, b string) float64 {
	sigA := minHashSignature(wordShingles(a))
	sigB := minHashSignature(wordShingles(b))
	return minHashSimilarity(sigA, sigB)
}

// CheckDedupMinHash runs the MinHash dedup layer. A thin wrapper over
// runScoringLayer that supplies the pairwise scoring function.
func CheckDedupMinHash(store *index.Store, description, repoRoot string) (*DedupResult, error) {
	return runScoringLayer(store, description, repoRoot, layerSpec{
		Name:        "minhash",
		Reason:      "minhash duplicate",
		Threshold:   minHashThreshold(),
		Score:       minHashScore,
		ScoreFormat: "jaccard≈%.2f",
	})
}
