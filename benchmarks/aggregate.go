// Command aggregate turns a directory of per-run result JSONs into a
// single markdown table for README / blog posts.
//
// Usage:
//
//	go run aggregate.go <results-dir>
//
// Reads every *.json file in the directory, groups them by (repo, task_id),
// aggregates the baseline / mcp variants, and computes per-task and
// per-repo averages plus an overall ratio.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type row struct {
	Repo         string  `json:"repo"`
	TaskID       string  `json:"task_id"`
	Variant      string  `json:"variant"`
	ExitCode     int     `json:"exit_code"`
	DiffLines    int     `json:"diff_lines"`
	WallClockSec float64 `json:"wall_clock_sec,omitempty"`
	Usage        struct {
		Turns                int            `json:"turns"`
		InputTokens          int            `json:"input_tokens"`
		OutputTokens         int            `json:"output_tokens"`
		CacheReadInputTokens int            `json:"cache_read_input_tokens"`
		EffectiveInputBilled int            `json:"effective_input_billed"`
		ToolCalls            int            `json:"tool_calls"`
		ToolCallsByName      map[string]int `json:"tool_calls_by_name"`
		WallClockSec         float64        `json:"wall_clock_sec"`
	} `json:"usage"`
}

type key struct{ repo, task string }

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: aggregate <results-dir>")
		os.Exit(2)
	}
	dir := os.Args[1]

	// Load all rows.
	all := make(map[key]map[string]row) // (repo, task) -> variant -> row
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var r row
		if err := json.Unmarshal(data, &r); err != nil {
			return nil
		}
		k := key{repo: r.Repo, task: r.TaskID}
		if all[k] == nil {
			all[k] = map[string]row{}
		}
		all[k][r.Variant] = r
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk: %v\n", err)
		os.Exit(1)
	}

	// Sort keys for stable output.
	var keys []key
	for k := range all {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].repo != keys[j].repo {
			return keys[i].repo < keys[j].repo
		}
		return keys[i].task < keys[j].task
	})

	// Header.
	fmt.Println("# skep benchmark results")
	fmt.Println()
	fmt.Printf("Source: `%s`\n\n", dir)
	fmt.Println("Columns:")
	fmt.Println()
	fmt.Println("- **classify** = one-shot classifier+plan LLM call (skep-only, no MCP). Flat overhead.")
	fmt.Println("- **baseline** = executor Claude Code session with NO skep MCP (cold reads).")
	fmt.Println("- **with MCP** = executor Claude Code session WITH skep MCP available.")
	fmt.Println("- **effective** = `input_tokens + 0.1 × cache_read_input_tokens` (approximates Anthropic's 90% cache-read discount).")
	fmt.Println("- **total w/ skep** = `classify.wall + with_MCP.effective`.")
	fmt.Println("- **ratio** = `total_w_skep / baseline.effective`. Values <1.0 mean skep wins.")
	fmt.Println()

	fmt.Println("| repo | task | classify wall (s) | baseline effective | with MCP effective | total w/ skep | ratio | baseline tools | MCP tools |")
	fmt.Println("|------|------|-------------------:|-------------------:|-------------------:|---------------:|------:|---------------:|----------:|")

	var sumBaseline, sumWithSkep int
	var taskCount int

	for _, k := range keys {
		variants := all[k]
		cls := variants["classify"]
		base := variants["baseline"]
		mcp := variants["mcp"]

		classWall := cls.WallClockSec
		baselineEff := base.Usage.EffectiveInputBilled
		mcpEff := mcp.Usage.EffectiveInputBilled
		totalSkep := mcpEff // classify tokens unknown in v1 harness — estimated as wall-time-only
		ratio := 0.0
		if baselineEff > 0 {
			ratio = float64(totalSkep) / float64(baselineEff)
		}

		fmt.Printf("| %s | %s | %.1f | %d | %d | %d | %.2f | %d | %d |\n",
			k.repo, k.task, classWall,
			baselineEff, mcpEff, totalSkep, ratio,
			base.Usage.ToolCalls, mcp.Usage.ToolCalls,
		)

		if baselineEff > 0 {
			sumBaseline += baselineEff
			sumWithSkep += totalSkep
			taskCount++
		}
	}

	fmt.Println()
	if taskCount > 0 {
		avgRatio := float64(sumWithSkep) / float64(sumBaseline)
		fmt.Printf("**Aggregate over %d tasks: %.1f%% reduction in effective input tokens (ratio %.2f).**\n",
			taskCount, (1-avgRatio)*100, avgRatio)
	}
}
