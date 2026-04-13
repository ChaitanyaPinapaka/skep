# Dedup benchmark

Evaluates the four cheap dedup layers (keyword → trigram → tf-idf → minhash)
against a labeled fixture of task-description pairs. Use it to:

- Tune the `SKEP_DEDUP_*_THRESHOLD` env vars for your repo.
- See which categories of duplicate each layer catches.
- Decide whether the LLM escape hatch earns its cost on your workload.

## Run

```bash
cd benchmarks/dedup
go run .
```

Optional: point at a different fixture.

```bash
go run . -pairs my-pairs.json
```

## Output

Two tables:

1. **Per-layer results** — `tp / fp / tn / fn / f1 / avg-ms`. F1 is the
   usual harmonic mean of precision and recall. `avg-ms` is the
   per-pair wall clock (candidate retrieval + similarity + log write).

2. **Per-category recall** — for each duplicate category in the
   fixture, what fraction each layer caught. This is the table that
   tells you where cheap layers fail and the LLM layer has to step
   in.

## Fixture format

`pairs.json`:

```json
{
  "pairs": [
    {"a": "add login endpoint", "b": "implement /auth/login", "duplicate": true, "category": "paraphrase"}
  ]
}
```

Categories are freeform strings; the benchmark groups results by
whatever you use. Recommended split:

| Category | What it tests |
|---|---|
| `exact` | Same string, case differences |
| `morphological` | "auth" vs "authentication" |
| `reorder` | Same words, reorganized |
| `paraphrase` | Same intent, different words |
| `synonym` | "dark mode" vs "dark theme" |
| `near-miss` | One word differs, intent differs |
| `direction-reversed` | A→B vs B→A |
| `shared-rare-word` | Coincidentally share a rare token |
| `same-surface-different-fix` | Same target, different change |
| `report-vs-task` | Bug report phrasing vs imperative task phrasing |

The fixture we ship has 38 hand-labeled pairs across these categories.

## Tuning thresholds

Each layer has an env-var threshold override:

| Layer | Env var | Default |
|---|---|---|
| keyword | `SKEP_DEDUP_BM25_THRESHOLD` | 0.80 |
| trigram | `SKEP_DEDUP_TRIGRAM_THRESHOLD` | 0.55 |
| tf-idf | `SKEP_DEDUP_TFIDF_THRESHOLD` | 0.60 |
| minhash | `SKEP_DEDUP_MINHASH_THRESHOLD` | 0.70 |

Re-run the bench with different values to see the precision/recall
tradeoff on your workload:

```bash
SKEP_DEDUP_TRIGRAM_THRESHOLD=0.40 go run .
```

Lower threshold → higher recall, more false positives. Start permissive
and tighten until the false-positive rate on your `near-miss` category
is acceptable.

## What this benchmark does NOT cover

- **LLM escape hatch.** The `CheckDedupLLM` layer is excluded because
  it shells out to Claude/Haiku and depends on network + API keys.
  Benchmark it separately with a scripted shell-out.
- **Cross-repo dedup.** `dedup_task` MCP tool runs the same layers —
  the results here apply transparently, but the benchmark only seeds
  a single local store.
- **Concurrency / race conditions.** The in-transaction re-check
  inside `tasks.Create` is tested by the unit suite, not here.
- **Performance under load.** Per-pair cost is reported but we do not
  measure the p99 at scale or the cost of a 1000-task active set.
