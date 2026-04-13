package tasks

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// clarifyDir is the subdirectory under .skep where clarification
// question files live. One file per task: `<id>.md`. Content is
// human-editable Markdown with a short header, the task description,
// and each question followed by a blank answer line.
func clarifyDir(skepDir string) string {
	return filepath.Join(skepDir, "clarify")
}

// ClarifyFilePath returns the absolute path of a task's clarify file.
// Does not check whether it exists.
func ClarifyFilePath(skepDir string, taskID int) string {
	return filepath.Join(clarifyDir(skepDir), fmt.Sprintf("%d.md", taskID))
}

// WriteClarifyFile writes a task's clarification questions to
// `.skep/clarify/<id>.md`. The file is human-editable: each question
// is followed by an empty `A:` line the user fills in. Returns the
// absolute path so the caller can print it in the "needs
// clarification" message.
//
// If the file already exists (e.g. the classifier ran twice before
// the user answered), it's overwritten — the latest classifier output
// is authoritative for the question set.
func WriteClarifyFile(skepDir string, taskID int, description string, questions []string) (string, error) {
	dir := clarifyDir(skepDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir clarify: %w", err)
	}
	path := ClarifyFilePath(skepDir, taskID)

	var b strings.Builder
	fmt.Fprintf(&b, "# Clarification needed for task #%d\n\n", taskID)
	fmt.Fprintf(&b, "Task: %s\n\n", description)
	b.WriteString("Answer each question on the `A:` line below it, then run:\n\n")
	fmt.Fprintf(&b, "    skep task clarify %d\n\n", taskID)
	b.WriteString("---\n\n")
	for i, q := range questions {
		fmt.Fprintf(&b, "Q%d: %s\nA%d: \n\n", i+1, q, i+1)
	}

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write clarify file: %w", err)
	}
	return path, nil
}

// ReadClarifyAnswers parses a clarify file and returns the list of
// (question, answer) pairs the user filled in. Questions with blank
// answers are included — the caller decides whether to reject an
// incomplete answer set.
//
// Parse rules: lines starting with `Q<n>:` are questions, the next
// `A<n>:` line holds the answer. Content after `A<n>:` up to the next
// `Q` line or EOF is the full answer (supporting multi-line).
func ReadClarifyAnswers(skepDir string, taskID int) ([]ClarifyQA, error) {
	path := ClarifyFilePath(skepDir, taskID)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open clarify file: %w", err)
	}
	defer f.Close()

	var pairs []ClarifyQA
	var current ClarifyQA
	var inAnswer bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Q") && strings.Contains(trimmed, ":"):
			// Flush the previous pair before starting a new one.
			if current.Question != "" {
				current.Answer = strings.TrimSpace(current.Answer)
				pairs = append(pairs, current)
			}
			current = ClarifyQA{Question: strings.TrimSpace(afterColon(trimmed))}
			inAnswer = false
		case strings.HasPrefix(trimmed, "A") && strings.Contains(trimmed, ":"):
			current.Answer = afterColon(trimmed)
			inAnswer = true
		case inAnswer && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "---"):
			// Continuation line for a multi-line answer.
			if current.Answer != "" {
				current.Answer += "\n"
			}
			current.Answer += line
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan clarify file: %w", err)
	}
	if current.Question != "" {
		current.Answer = strings.TrimSpace(current.Answer)
		pairs = append(pairs, current)
	}
	return pairs, nil
}

// ClarifyQA is one question/answer pair from a clarify file.
type ClarifyQA struct {
	Question string
	Answer   string
}

// afterColon returns the substring after the first colon in s,
// trimmed. Used for Q/A line parsing where the prefix is "Q1:" or
// "A1:" followed by the content.
func afterColon(s string) string {
	i := strings.IndexByte(s, ':')
	if i < 0 || i+1 >= len(s) {
		return ""
	}
	return strings.TrimSpace(s[i+1:])
}

// BuildClarifiedDescription weaves the answers back into the original
// task description so the re-run classifier sees the full context.
// Format:
//
//	<original task>
//
//	Clarifications:
//	- <question>: <answer>
//	- <question>: <answer>
//
// Empty answers are dropped — no point re-running classify against
// half-filled clarifications.
func BuildClarifiedDescription(original string, answers []ClarifyQA) string {
	var b strings.Builder
	b.WriteString(original)
	b.WriteString("\n\nClarifications:\n")
	wrote := false
	for _, qa := range answers {
		if qa.Answer == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", qa.Question, qa.Answer)
		wrote = true
	}
	if !wrote {
		return original
	}
	return b.String()
}
