package tasks

import "testing"

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean json", `{"classification":"small"}`, `{"classification":"small"}`},
		{"markdown fenced", "```json\n{\"classification\":\"small\"}\n```", `{"classification":"small"}`},
		{"text before", "Here is my analysis:\n{\"classification\":\"large\"}", `{"classification":"large"}`},
		{"text after", "{\"classification\":\"small\"}\nDone.", `{"classification":"small"}`},
		{"no json", "just some text", ""},
		{"empty", "", ""},
		{"nested braces", `{"plan":["step {1}","step 2"]}`, `{"plan":["step {1}","step 2"]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
