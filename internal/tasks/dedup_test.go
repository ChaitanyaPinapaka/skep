package tasks

import (
	"math"
	"strings"
	"testing"
)

func TestKeywordMatch(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"exact match", "add export endpoint", "add export endpoint", true},
		{"high overlap", "add export endpoint", "add export endpoint for users", true},
		{"low overlap", "add export endpoint", "fix login bug", false},
		{"empty a", "", "add export endpoint", false},
		{"empty b", "add export endpoint", "", false},
		{"both empty", "", "", false},
		{"short words filtered", "a b c d", "a b c d", false},
		{"case insensitive", "Add Export Endpoint", "add export endpoint", true},
		{"partial overlap below threshold", "add new export endpoint handler", "add user profile handler", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keywordMatch(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("keywordMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestKeywordOverlapScore(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"add login endpoint", "add login endpoint", 1.0},
		{"add login endpoint", "refactor scheduler", 0.0},
		{"add login", "please add a login endpoint to the api", 1.0},
		{"", "anything", 0.0},
	}
	for _, c := range cases {
		got := keywordOverlap(c.a, c.b)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("keywordOverlap(%q,%q) = %f; want ~%f", c.a, c.b, got, c.want)
		}
	}
}

func TestTrigramJaccard(t *testing.T) {
	if got := trigramJaccard("add login endpoint", "add login endpoint"); math.Abs(got-1.0) > 0.001 {
		t.Fatalf("identical trigram jaccard = %f, want 1.0", got)
	}
	if got := trigramJaccard("add auth endpoint", "add authentication endpoint"); got < 0.5 {
		t.Fatalf("auth vs authentication trigram = %f, want >= 0.5", got)
	}
	if got := trigramJaccard("add login", "delete foo"); got > 0.25 {
		t.Fatalf("disjoint trigram = %f, want < 0.25", got)
	}
	if math.Abs(trigramJaccard("fix login", "login fix")-trigramJaccard("login fix", "fix login")) > 0.001 {
		t.Fatal("trigramJaccard not symmetric")
	}
}

func TestCosineSimTFIDF(t *testing.T) {
	candidates := []*Task{
		{ID: 1, Description: "add login endpoint"},
		{ID: 2, Description: "refactor scheduler loop"},
		{ID: 3, Description: "update readme installation"},
	}
	candVecs, newVec := buildTFIDFCorpus(candidates, "add login endpoint")
	if score := cosineSim(candVecs[1], newVec); score < 0.99 {
		t.Fatalf("exact tfidf cosine = %f, want ~1.0", score)
	}
	if score := cosineSim(candVecs[2], newVec); score > 0.1 {
		t.Fatalf("unrelated tfidf cosine = %f, want <0.1", score)
	}
}

func TestMinHashSimilarity(t *testing.T) {
	a := minHashSignature(wordShingles("add login endpoint please"))
	b := minHashSignature(wordShingles("add login endpoint please"))
	if s := minHashSimilarity(a, b); s != 1.0 {
		t.Fatalf("identical minhash = %f, want 1.0", s)
	}
	d := minHashSignature(wordShingles("refactor scheduler loop quickly"))
	if s := minHashSimilarity(a, d); s > 0.2 {
		t.Fatalf("disjoint minhash = %f, want <0.2", s)
	}
}

func TestFTS5SanitizeQuery(t *testing.T) {
	// Empty in → empty out.
	if got := fts5SanitizeQuery(""); got != "" {
		t.Errorf("empty sanitize = %q, want empty", got)
	}
	// Tokens must be quoted.
	got := fts5SanitizeQuery(`add "login" endpoint`)
	if !strings.Contains(got, `"login"`) || !strings.Contains(got, `"add"`) || !strings.Contains(got, `"endpoint"`) {
		t.Errorf("sanitize = %q, missing quoted tokens", got)
	}
	// FTS5 operators must not pass through verbatim.
	got = fts5SanitizeQuery("NEAR(foo, bar)")
	for _, forbidden := range []string{"NEAR(", "(", ")"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("sanitize leaked %q: %q", forbidden, got)
		}
	}
	// Colons (column filters) must not pass through.
	if strings.Contains(fts5SanitizeQuery("col:value"), ":") {
		t.Error("sanitize leaked colon")
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"add export endpoint", 3},
		{"a b c", 0},
		{"hello world", 2},
		{"fix the N+1 query in feed handler", 5},
		{"", 0},
	}
	for _, tt := range tests {
		tokens := tokenize(tt.input)
		if len(tokens) != tt.want {
			t.Errorf("tokenize(%q) = %d tokens %v, want %d", tt.input, len(tokens), tokens, tt.want)
		}
	}
}

func TestThresholdEnvOverrides(t *testing.T) {
	t.Setenv("SKEP_DEDUP_BM25_THRESHOLD", "0.9")
	if got := bm25Threshold(); math.Abs(got-0.9) > 0.001 {
		t.Fatalf("bm25Threshold env override = %f, want 0.9", got)
	}
	t.Setenv("SKEP_DEDUP_BM25_THRESHOLD", "not-a-number")
	if got := bm25Threshold(); math.Abs(got-defaultBM25Threshold) > 0.001 {
		t.Fatalf("bm25Threshold invalid env = %f, want default %f", got, defaultBM25Threshold)
	}
	t.Setenv("SKEP_DEDUP_BM25_THRESHOLD", "2.5")
	if got := bm25Threshold(); math.Abs(got-defaultBM25Threshold) > 0.001 {
		t.Fatalf("bm25Threshold out-of-range env = %f, want default %f", got, defaultBM25Threshold)
	}
}

func TestDedupLogOneLine(t *testing.T) {
	got := oneLine("line\nwith\ttabs\r\nand  spaces", 100)
	if got != "line with tabs and spaces" {
		t.Fatalf("oneLine = %q", got)
	}
	if g := oneLine("abcdef", 3); g != "abc…" {
		t.Fatalf("oneLine truncate = %q", g)
	}
}
