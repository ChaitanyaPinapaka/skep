package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/llm"
	"github.com/ChaitanyaPinapaka/skep/internal/llm/prompts"
)

// DedupResult indicates why a task was deduplicated. Layer records which
// stage of the dedup pipeline made the call so observability tooling can
// answer "how often does the LLM layer earn its cost?" without parsing
// prose reasons.
type DedupResult struct {
	IsDuplicate bool    `json:"is_duplicate"`
	Reason      string  `json:"reason"`
	TaskID      int     `json:"task_id,omitempty"`
	Layer       string  `json:"layer,omitempty"` // keyword | trigram | tfidf | minhash | llm
	Score       float64 `json:"score,omitempty"` // layer-specific similarity, 0..1 where meaningful
}

// defaultBM25Threshold is the word-overlap ratio above which the keyword
// layer flags a duplicate. Overridable via SKEP_DEDUP_BM25_THRESHOLD —
// repos with terse descriptions may want this lower, verbose repos higher.
const defaultBM25Threshold = 0.8

// defaultDedupTimeout bounds the LLM semantic dedup call. Lower than the
// classifier timeout because dedup is advisory — if it takes more than
// a minute the right call is to give up and let the task through, not
// block the user's shell.
const defaultDedupTimeout = 60 * time.Second

// bm25Threshold returns the configured word-overlap threshold, reading
// SKEP_DEDUP_BM25_THRESHOLD if set and clamping to (0, 1].
func bm25Threshold() float64 {
	v := strings.TrimSpace(os.Getenv("SKEP_DEDUP_BM25_THRESHOLD"))
	if v == "" {
		return defaultBM25Threshold
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f > 1 {
		return defaultBM25Threshold
	}
	return f
}

// dedupTimeout returns the configured LLM dedup timeout, reading
// SKEP_DEDUP_TIMEOUT (seconds) if set. Zero or unset → default.
func dedupTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("SKEP_DEDUP_TIMEOUT"))
	if v == "" {
		return defaultDedupTimeout
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return defaultDedupTimeout
	}
	return time.Duration(secs) * time.Second
}

// CheckDedup checks if a task duplicates an existing active task using
// the fast keyword-overlap path. Logs every check to .skep/log/dedup.log
// when repoRoot is provided. Use CheckDedupSilent from inside transactions
// or other paths where the log call is noise.
func CheckDedup(store *index.Store, description string) (*DedupResult, error) {
	return CheckDedupInRoot(store, description, "")
}

// CheckDedupInRoot is CheckDedup with log-file location. Called from cmd/
// where repoRoot is already in hand.
func CheckDedupInRoot(store *index.Store, description, repoRoot string) (*DedupResult, error) {
	start := time.Now()
	threshold := bm25Threshold()

	// Scan active statuses in priority order: executing first (real-time
	// collision matters most), then queued, then created/classified.
	for _, status := range []string{StatusExecuting, StatusPending, StatusApproved, StatusCreated, StatusClassified} {
		tasks, _ := ListByStatus(store, status)
		for _, t := range tasks {
			score := keywordOverlap(t.Description, description)
			if score >= threshold {
				reason := fmt.Sprintf("in progress as task #%d", t.ID)
				if status != StatusExecuting {
					reason = fmt.Sprintf("already queued as task #%d (status: %s)", t.ID, t.Status)
				}
				r := &DedupResult{
					IsDuplicate: true,
					Reason:      reason,
					TaskID:      t.ID,
					Layer:       "keyword",
					Score:       score,
				}
				LogDedup(repoRoot, DedupLogEntry{
					Layer:       "keyword",
					Hit:         true,
					Score:       score,
					TaskDesc:    description,
					CandidateID: t.ID,
					Duration:    time.Since(start),
					Reason:      reason,
				})
				return r, nil
			}
		}
	}

	LogDedup(repoRoot, DedupLogEntry{
		Layer:    "keyword",
		Hit:      false,
		TaskDesc: description,
		Duration: time.Since(start),
	})
	return &DedupResult{IsDuplicate: false, Layer: "keyword"}, nil
}

// keywordMatch is a thin wrapper retained for call sites that just want
// a boolean. New code should prefer keywordOverlap + threshold comparison
// so the score can be logged and the threshold can come from config.
func keywordMatch(a, b string) bool {
	return keywordOverlap(a, b) >= bm25Threshold()
}

// keywordOverlap returns the fraction of tokens in a that also appear
// in b. Returns 0 when either side has no tokens.
//
// This is intentionally asymmetric: if a has 4 words and b has 40 and
// all 4 overlap, that's a 1.0 match — the short description is probably
// the duplicate-of-record. We measure a→b overlap rather than Jaccard
// because task descriptions vary wildly in length and Jaccard punishes
// that.
func keywordOverlap(a, b string) float64 {
	wordsA := tokenize(a)
	wordsB := tokenize(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0
	}
	setB := make(map[string]bool, len(wordsB))
	for _, w := range wordsB {
		setB[w] = true
	}
	matches := 0
	for _, w := range wordsA {
		if setB[w] {
			matches++
		}
	}
	return float64(matches) / float64(len(wordsA))
}

func tokenize(s string) []string {
	var words []string
	word := make([]byte, 0, 32)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAlphaNum(c) {
			if c >= 'A' && c <= 'Z' {
				c += 32
			}
			word = append(word, c)
		} else {
			if len(word) > 2 {
				words = append(words, string(word))
			}
			word = word[:0]
		}
	}
	if len(word) > 2 {
		words = append(words, string(word))
	}
	return words
}

func isAlphaNum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// fts5SanitizeQuery escapes a free-form user string for safe inclusion
// in an FTS5 MATCH expression. FTS5 has its own mini query language
// (operators: AND OR NOT NEAR, phrase quoting, column filters with ':',
// prefix '*'); passing raw task descriptions into MATCH breaks on any
// of those metacharacters.
//
// We take the conservative route: extract word tokens, double-quote
// each one (which makes it a phrase in FTS5 syntax and disables
// operator interpretation), and OR them together. Any token containing
// a double quote gets that quote doubled, per FTS5 phrase escaping
// rules.
//
// Not currently wired into any dedup layer — the four cheap layers
// (keyword, trigram, tf-idf, minhash) score candidates in Go memory
// rather than via FTS5 MATCH, so this helper exists as infrastructure
// for a future FTS5-backed candidate prefilter. Kept covered by tests
// so it stays correct if/when a caller needs it.
func fts5SanitizeQuery(s string) string {
	tokens := tokenize(s)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, t := range tokens {
		// Escape embedded double quotes by doubling them — FTS5 phrase rule.
		t = strings.ReplaceAll(t, `"`, `""`)
		quoted = append(quoted, `"`+t+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// activeTaskCandidates returns all non-terminal tasks suitable for
// dedup comparison. One NOT-IN query replaces seven per-status
// queries — the previous implementation ran 7 round-trips per call
// and was called at least 3 times per `task create` (keyword, LLM
// escape hatch, MCP dedup_task). On a 500-task repo this was the
// dominant cost of a create.
func activeTaskCandidates(store *index.Store) []*Task {
	rows, err := queryTasks(store.DB(), `
		SELECT id, name, description, status, session_id, classification, plan,
		       source_repo, source_task_id, created_by, acceptance, result, branch,
		       tokens_used, tools_used_json, plan_json, needs_input, created_at, updated_at
		FROM tasks
		WHERE status NOT IN (?, ?, ?, ?)
		ORDER BY created_at DESC`,
		StatusDone, StatusFailed, StatusRejected, StatusPendingClarification,
	)
	if err != nil {
		return nil
	}
	return rows
}

// CheckDedupLLM runs a second-pass duplicate check using the classify LLM.
// Non-cancellable wrapper around CheckDedupLLMCtx that applies the
// configured SKEP_DEDUP_TIMEOUT so a hung LLM CLI cannot wedge a shell.
func CheckDedupLLM(store *index.Store, description, repoRoot, llmCmd string) (*DedupResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dedupTimeout())
	defer cancel()
	return CheckDedupLLMCtx(ctx, store, description, repoRoot, llmCmd)
}

// CheckDedupLLMCtx is the cancellable, logged variant of the LLM
// semantic dedup check. Logs the outcome (hit/miss/err) to
// .skep/log/dedup.log regardless of result — advisory semantics stay
// intact (errors return non-duplicate) but we no longer swallow them
// silently.
func CheckDedupLLMCtx(ctx context.Context, store *index.Store, description, repoRoot, llmCmd string) (*DedupResult, error) {
	start := time.Now()

	miss := func(err error, reason string) *DedupResult {
		LogDedup(repoRoot, DedupLogEntry{
			Layer:    "llm",
			Hit:      false,
			TaskDesc: description,
			Duration: time.Since(start),
			Err:      err,
			Reason:   reason,
		})
		return &DedupResult{IsDuplicate: false, Layer: "llm"}
	}

	if llmCmd == "" {
		return miss(nil, "llm-cmd unset"), nil
	}

	candidates := activeTaskCandidates(store)
	if len(candidates) == 0 {
		return miss(nil, "no active candidates"), nil
	}

	// Pre-filter: rank candidates by keyword overlap with the new
	// description, keep the top 5. Keeps the LLM prompt small even when
	// a repo has hundreds of active tasks.
	type scored struct {
		task  *Task
		score int
	}
	descWords := tokenize(description)
	descSet := make(map[string]bool, len(descWords))
	for _, w := range descWords {
		descSet[w] = true
	}
	ranked := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		overlap := 0
		for _, w := range tokenize(c.Description) {
			if descSet[w] {
				overlap++
			}
		}
		ranked = append(ranked, scored{task: c, score: overlap})
	}
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j-1].score < ranked[j].score; j-- {
			ranked[j-1], ranked[j] = ranked[j], ranked[j-1]
		}
	}
	topN := 5
	if len(ranked) < topN {
		topN = len(ranked)
	}
	refs := make([]prompts.ExistingTaskRef, 0, topN)
	for _, s := range ranked[:topN] {
		refs = append(refs, prompts.ExistingTaskRef{
			ID:          s.task.ID,
			Status:      s.task.Status,
			Description: s.task.Description,
		})
	}

	prompt, err := prompts.Dedup(prompts.DedupData{
		Task:       description,
		Candidates: refs,
	})
	if err != nil {
		return miss(fmt.Errorf("render dedup prompt: %w", err), "prompt render failed"), nil
	}

	cmd := classifyCommand(llmCmd)
	output, err := llm.ShellOutQuietCtx(ctx, repoRoot, cmd, prompt)
	if err != nil {
		// Error is surfaced to the log; behavior stays advisory (non-dup).
		return miss(err, "llm shell-out failed"), nil
	}

	var resp struct {
		Duplicate   bool   `json:"duplicate"`
		DuplicateOf int    `json:"duplicate_of"`
		Reason      string `json:"reason"`
	}
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return miss(fmt.Errorf("no json in llm output"), "unparseable llm output"), nil
	}
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return miss(err, "llm json decode failed"), nil
	}
	if resp.Duplicate && resp.DuplicateOf == 0 {
		// LLM said "yes duplicate" but couldn't name which — log loudly,
		// treat as non-duplicate. This is the "unresolvable" case; useful
		// signal for benchmark tuning.
		return miss(nil, fmt.Sprintf("llm flagged duplicate but no id: %s", resp.Reason)), nil
	}
	if !resp.Duplicate || resp.DuplicateOf == 0 {
		LogDedup(repoRoot, DedupLogEntry{
			Layer:    "llm",
			Hit:      false,
			TaskDesc: description,
			Duration: time.Since(start),
			Reason:   resp.Reason,
		})
		return &DedupResult{IsDuplicate: false, Layer: "llm"}, nil
	}

	reason := strings.TrimSpace(resp.Reason)
	if reason == "" {
		reason = fmt.Sprintf("semantically duplicates task #%d", resp.DuplicateOf)
	} else {
		reason = fmt.Sprintf("%s (task #%d)", reason, resp.DuplicateOf)
	}
	LogDedup(repoRoot, DedupLogEntry{
		Layer:       "llm",
		Hit:         true,
		Score:       1.0,
		TaskDesc:    description,
		CandidateID: resp.DuplicateOf,
		Duration:    time.Since(start),
		Reason:      reason,
	})
	return &DedupResult{
		IsDuplicate: true,
		Reason:      reason,
		TaskID:      resp.DuplicateOf,
		Layer:       "llm",
		Score:       1.0,
	}, nil
}
