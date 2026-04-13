package index

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WalkResult holds information about a discovered file.
type WalkResult struct {
	AbsPath  string
	RelPath  string
	Size     int64
	ModTime  int64
	Language string
}

// WalkRepo discovers source files in a repo. Uses git ls-files if the repo
// has git (10x faster, respects .gitignore natively). Falls back to WalkDir
// for non-git repos.
func WalkRepo(root string) ([]WalkResult, error) {
	if isGitRepo(root) {
		return walkGit(root)
	}
	return walkDir(root)
}

// walkGit uses `git ls-files` — returns only tracked + untracked source files,
// respects .gitignore natively, no custom ignore parsing needed.
func walkGit(root string) ([]WalkResult, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root

	// Stream output line-by-line instead of buffering entire output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return walkDir(root)
	}
	if err := cmd.Start(); err != nil {
		return walkDir(root)
	}

	var results []WalkResult
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		rel := strings.TrimSpace(scanner.Text())
		if rel == "" {
			continue
		}

		lang := detectLanguage(rel)
		if lang == "" {
			continue
		}

		absPath := filepath.Join(root, rel)
		// Use Lstat to detect symlinks — don't follow them.
		// Symlinks could point outside the repo or to /etc/passwd etc.
		info, err := os.Lstat(absPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue // skip symlinks
		}

		results = append(results, WalkResult{
			AbsPath:  absPath,
			RelPath:  rel,
			Size:     info.Size(),
			ModTime:  info.ModTime().Unix(),
			Language: lang,
		})
	}

	if err := cmd.Wait(); err != nil {
		// git failed mid-stream — return what we have
		if len(results) == 0 {
			return walkDir(root)
		}
	}
	return results, nil
}

// walkDir is the fallback for non-git repos. Uses WalkDir (not Walk) to avoid
// an extra os.Stat per directory entry.
func walkDir(root string) ([]WalkResult, error) {
	ignores := loadGitignore(root)
	ignores = append(ignores, ".git", ".skep", "node_modules", "vendor", "__pycache__", ".next", "dist", "build")

	var results []WalkResult

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		for _, pattern := range ignores {
			if matchIgnore(rel, pattern, d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if d.IsDir() {
			return nil
		}

		lang := detectLanguage(path)
		if lang == "" {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		// Skip symlinks — don't follow them
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		results = append(results, WalkResult{
			AbsPath:  path,
			RelPath:  rel,
			Size:     info.Size(),
			ModTime:  info.ModTime().Unix(),
			Language: lang,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return results, nil
}

func isGitRepo(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = root
	return cmd.Run() == nil
}

func loadGitignore(root string) []string {
	path := filepath.Join(root, ".gitignore")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

func matchIgnore(relPath, pattern string, isDir bool) bool {
	dirOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")

	if dirOnly && !isDir {
		parts := strings.Split(relPath, string(filepath.Separator))
		for _, p := range parts[:len(parts)-1] {
			if matched, _ := filepath.Match(pattern, p); matched {
				return true
			}
		}
		return false
	}

	base := filepath.Base(relPath)
	if matched, _ := filepath.Match(pattern, base); matched {
		return true
	}

	if matched, _ := filepath.Match(pattern, relPath); matched {
		return true
	}

	parts := strings.Split(relPath, string(filepath.Separator))
	for _, p := range parts {
		if matched, _ := filepath.Match(pattern, p); matched {
			return true
		}
	}

	return false
}

var langExtensions = map[string]string{
	".go":     "go",
	".ts":     "typescript",
	".tsx":    "typescript",
	".js":     "javascript",
	".jsx":    "javascript",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".py":     "python",
	".java":   "java",
	".kt":     "kotlin",
	".kts":    "kotlin",
	".yml":    "yaml",
	".yaml":   "yaml",
	".tf":     "terraform",
	".tfvars": "terraform",
	// JSON configs — limited to well-known config filenames in detectLanguage
	// Rust / Ruby / C / C++ / C# — file-level only for now
	".rs":    "rust",
	".rb":    "ruby",
	".c":     "c",
	".h":     "c",
	".cpp":   "cpp",
	".hpp":   "cpp",
	".cs":    "csharp",
	".proto": "protobuf",
	".sql":   "sql",
}

// detectLanguage returns the language name for a file path.
// Handles special filenames (Dockerfile, package.json, etc.) that don't map by extension.
func detectLanguage(path string) string {
	base := strings.ToLower(filepath.Base(path))

	// Dockerfile variants: Dockerfile, Dockerfile.prod, Dockerfile.admin, *.dockerfile
	if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") || strings.HasSuffix(base, ".dockerfile") {
		return "dockerfile"
	}

	// JSON configs we understand — top-level key extraction is useful
	jsonConfigs := map[string]bool{
		"package.json": true, "tsconfig.json": true, "composer.json": true,
		"deno.json": true, "biome.json": true, "turbo.json": true, "nx.json": true,
	}
	if jsonConfigs[base] {
		return "json"
	}

	ext := strings.ToLower(filepath.Ext(path))
	return langExtensions[ext]
}
