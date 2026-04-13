package agent

import (
	"fmt"
	"os/exec"
	"strings"
)

// GitCurrentBranch returns the current git branch name.
func GitCurrentBranch(repoRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git current branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GitCheckoutBranch creates and checks out a new branch.
func GitCheckoutBranch(repoRoot, branch string) error {
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout -b %s: %w\n%s", branch, err, string(out))
	}
	return nil
}

// GitCheckout checks out an existing branch.
func GitCheckout(repoRoot, branch string) error {
	cmd := exec.Command("git", "checkout", branch)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout %s: %w\n%s", branch, err, string(out))
	}
	return nil
}

// GitAllCommitsInBase returns true if every commit on taskBranch is reachable
// from baseBranch — meaning the work has been merged into base.
func GitAllCommitsInBase(repoRoot, baseBranch, taskBranch string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", taskBranch, baseBranch)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

// GitCurrentBranchOr returns the current branch or fallback if git fails.
func GitCurrentBranchOr(repoRoot, fallback string) (string, error) {
	b, err := GitCurrentBranch(repoRoot)
	if err != nil || b == "" {
		return fallback, err
	}
	return b, nil
}

// GitIsClean returns true if the working tree has no uncommitted changes.
// Ignores .skep/ directory (created by skep, not user work).
func GitIsClean(repoRoot string) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Ignore .skep/ — it's ours, not user work
		if strings.Contains(line, ".skep") {
			continue
		}
		return false // found a non-skep dirty file
	}
	return true
}
