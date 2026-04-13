// Command dedup-bench evaluates the four cheap dedup layers against a
// labeled fixture of task-description pairs. For each pair it seeds
// the `a` description as an active task in a fresh temporary SQLite
// store, then runs each layer against `b` and records whether that
// layer flagged a duplicate.
//
// Output: per-layer precision / recall / F1, per-category breakdown,
// and mean wall-clock cost. Use to tune SKEP_DEDUP_*_THRESHOLD
// values on your repo's real task history.
//
// Usage:
//
//	cd benchmarks/dedup
//	go run . [-pairs pairs.json]
//
// Does NOT invoke any LLM — only the four cheap layers. The LLM
// escape hatch is evaluated separately (see benchmarks/ README).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ChaitanyaPinapaka/skep/internal/index"
	"github.com/ChaitanyaPinapaka/skep/internal/tasks"
)

type pair struct {
	A         string `json:"a"`
	B         string `json:"b"`
	Duplicate bool   `json:"duplicate"`
	Category  string `json:"category"`
}

type pairsFile struct {
	Pairs []pair `json:"pairs"`
}

// layerStats accumulates true/false positives+negatives per layer.
type layerStats struct {
	tp, fp, tn, fn int
	totalDuration  time.Duration
}

func (s layerStats) precision() float64 {
	if s.tp+s.fp == 0 {
		return 0
	}
	return float64(s.tp) / float64(s.tp+s.fp)
}

func (s layerStats) recall() float64 {
	if s.tp+s.fn == 0 {
		return 0
	}
	return float64(s.tp) / float64(s.tp+s.fn)
}

func (s layerStats) f1() float64 {
	p, r := s.precision(), s.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

func main() {
	pairsPath := flag.String("pairs", "pairs.json", "labeled dedup pairs fixture")
	flag.Parse()

	raw, err := os.ReadFile(*pairsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read pairs: %v\n", err)
		os.Exit(1)
	}
	var pf pairsFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		fmt.Fprintf(os.Stderr, "parse pairs: %v\n", err)
		os.Exit(1)
	}

	// Per-layer and per-category stats.
	layers := []string{"keyword", "trigram", "tfidf", "minhash"}
	stats := make(map[string]*layerStats, len(layers))
	for _, l := range layers {
		stats[l] = &layerStats{}
	}
	catStats := make(map[string]map[string]*layerStats) // cat → layer → stats

	runners := map[string]func(*index.Store, string, string) (*tasks.DedupResult, error){
		"keyword": tasks.CheckDedupInRoot,
		"trigram": tasks.CheckDedupTrigram,
		"tfidf":   tasks.CheckDedupTFIDF,
		"minhash": tasks.CheckDedupMinHash,
	}

	for i, p := range pf.Pairs {
		// Fresh temp store per pair so seeded task IDs don't collide.
		dir, err := os.MkdirTemp("", "skep-dedup-bench-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "mktemp: %v\n", err)
			os.Exit(1)
		}
		store, err := index.OpenStore(filepath.Join(dir, "skep.db"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "open store: %v\n", err)
			os.Exit(1)
		}

		// Seed `a` as a pending task.
		if _, _, err := tasks.Create(store, p.A, "", ""); err != nil {
			fmt.Fprintf(os.Stderr, "seed %d: %v\n", i, err)
			store.Close()
			os.RemoveAll(dir)
			continue
		}

		// Run each layer against `b`.
		for _, layer := range layers {
			start := time.Now()
			r, _ := runners[layer](store, p.B, "")
			elapsed := time.Since(start)
			hit := r != nil && r.IsDuplicate

			s := stats[layer]
			s.totalDuration += elapsed
			switch {
			case hit && p.Duplicate:
				s.tp++
			case hit && !p.Duplicate:
				s.fp++
			case !hit && p.Duplicate:
				s.fn++
			case !hit && !p.Duplicate:
				s.tn++
			}

			if catStats[p.Category] == nil {
				catStats[p.Category] = make(map[string]*layerStats)
			}
			if catStats[p.Category][layer] == nil {
				catStats[p.Category][layer] = &layerStats{}
			}
			cs := catStats[p.Category][layer]
			switch {
			case hit && p.Duplicate:
				cs.tp++
			case hit && !p.Duplicate:
				cs.fp++
			case !hit && p.Duplicate:
				cs.fn++
			case !hit && !p.Duplicate:
				cs.tn++
			}
		}
		store.Close()
		os.RemoveAll(dir)
	}

	fmt.Println("Skep dedup benchmark")
	fmt.Printf("Corpus: %d pairs from %s\n\n", len(pf.Pairs), *pairsPath)

	// Overall table.
	fmt.Println("Per-layer results:")
	fmt.Printf("%-10s %6s %6s %6s %6s %10s %12s\n", "layer", "tp", "fp", "tn", "fn", "f1", "avg-ms")
	for _, l := range layers {
		s := stats[l]
		avg := float64(0)
		if n := s.tp + s.fp + s.tn + s.fn; n > 0 {
			avg = float64(s.totalDuration.Microseconds()) / float64(n) / 1000.0
		}
		fmt.Printf("%-10s %6d %6d %6d %6d %10.3f %12.3f\n",
			l, s.tp, s.fp, s.tn, s.fn, s.f1(), avg)
	}
	fmt.Println()

	// Per-category F1.
	fmt.Println("Per-category recall (catches true duplicates):")
	cats := make([]string, 0, len(catStats))
	for c := range catStats {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	fmt.Printf("%-24s", "category")
	for _, l := range layers {
		fmt.Printf(" %10s", l)
	}
	fmt.Println()
	for _, c := range cats {
		fmt.Printf("%-24s", c)
		for _, l := range layers {
			cs := catStats[c][l]
			if cs == nil {
				fmt.Printf(" %10s", "-")
				continue
			}
			fmt.Printf(" %10.3f", cs.recall())
		}
		fmt.Println()
	}
}
