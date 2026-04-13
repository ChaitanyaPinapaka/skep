// Command measure reads a Claude Code session JSONL file and emits
// aggregate usage statistics as JSON.
//
// Usage:
//
//	measure <session.jsonl>
//
// The output schema is stable across runs so downstream tooling (bench.sh,
// results.md generator) can rely on field names.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type sessionEntry struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// usage fields are sent by the Anthropic API inside each assistant message.
type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// content block inside an assistant message — used to count tool calls.
type contentBlock struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type assistantMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
	Usage   usage          `json:"usage"`
}

type aggregate struct {
	Turns                    int            `json:"turns"`
	InputTokens              int            `json:"input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens"`
	EffectiveInputBilled     int            `json:"effective_input_billed"`
	ToolCalls                int            `json:"tool_calls"`
	ToolCallsByName          map[string]int `json:"tool_calls_by_name"`
	FirstTurnAt              string         `json:"first_turn_at,omitempty"`
	LastTurnAt               string         `json:"last_turn_at,omitempty"`
	WallClockSec             float64        `json:"wall_clock_sec"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: measure <session.jsonl>")
		os.Exit(2)
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	agg := aggregate{ToolCallsByName: map[string]int{}}

	scanner := bufio.NewScanner(f)
	// Claude session lines can be large — bump the buffer.
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var firstTurn, lastTurn time.Time

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry sessionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" || len(entry.Message) == 0 {
			continue
		}

		var msg assistantMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}
		if msg.Role != "assistant" {
			continue
		}

		agg.Turns++
		agg.InputTokens += msg.Usage.InputTokens
		agg.OutputTokens += msg.Usage.OutputTokens
		agg.CacheCreationInputTokens += msg.Usage.CacheCreationInputTokens
		agg.CacheReadInputTokens += msg.Usage.CacheReadInputTokens

		for _, c := range msg.Content {
			if c.Type == "tool_use" {
				agg.ToolCalls++
				if c.Name != "" {
					agg.ToolCallsByName[c.Name]++
				}
			}
		}

		if entry.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				if firstTurn.IsZero() || t.Before(firstTurn) {
					firstTurn = t
				}
				if t.After(lastTurn) {
					lastTurn = t
				}
			}
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}

	if !firstTurn.IsZero() {
		agg.FirstTurnAt = firstTurn.Format(time.RFC3339)
	}
	if !lastTurn.IsZero() {
		agg.LastTurnAt = lastTurn.Format(time.RFC3339)
	}
	if !firstTurn.IsZero() && !lastTurn.IsZero() {
		agg.WallClockSec = lastTurn.Sub(firstTurn).Seconds()
	}

	// Effective billed input tokens = uncached + 0.1 × cached_reads
	// (approximation of Anthropic's 90% cache-read discount).
	agg.EffectiveInputBilled = agg.InputTokens + agg.CacheReadInputTokens/10

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(agg)
}
