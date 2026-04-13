package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ChaitanyaPinapaka/skep/internal/mcp"
)

// cmdMCP dispatches `skep mcp [verb]`.
// Verbs: serve (default — runs the stdio MCP server, used by Claude Code);
//
//	install (writes/merges the skep server entry into ~/.claude/settings.json).
func cmdMCP(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "install":
			return cmdMCPInstall(args[1:])
		case "serve":
			return cmdMCPServe(args[1:])
		}
	}
	return cmdMCPServe(args)
}

func cmdMCPServe(args []string) error {
	// Support --repo flag for pointing to a specific repo
	var repoPath string
	for i, a := range args {
		if a == "--repo" && i+1 < len(args) {
			repoPath = args[i+1]
			break
		}
	}

	var root string
	var err error
	if repoPath != "" {
		root = repoPath
	} else {
		root, err = repoRoot()
		if err != nil {
			return err
		}
	}

	rdir := filepath.Join(root, ".skep")
	return mcp.Run(root, rdir)
}

// cmdMCPInstall writes the skep MCP server entry into ~/.claude/settings.json.
// Preserves existing mcpServers entries and any unrelated top-level keys.
// Uses the absolute path to the currently running skep binary so the Claude
// settings keep working even if $PATH changes.
func cmdMCPInstall(args []string) error {
	name := "skep"
	for i, a := range args {
		if a == "--name" && i+1 < len(args) {
			name = args[i+1]
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Resolve the absolute path to this skep binary.
	exe, err := os.Executable()
	if err != nil {
		// Fall back to looking up "skep" on PATH.
		if p, lerr := exec.LookPath("skep"); lerr == nil {
			exe = p
		} else {
			return fmt.Errorf("resolve skep binary: %w", err)
		}
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}

	// Load existing settings (may not exist, may be empty).
	settings := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}

	// Fetch or create mcpServers map.
	servers, _ := settings["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}

	// Detect conflict — refuse unless --force.
	force := false
	for _, a := range args {
		if a == "--force" {
			force = true
		}
	}
	if existing, ok := servers[name]; ok && !force {
		fmt.Fprintf(os.Stderr, "mcpServers.%s already exists in %s\n", name, settingsPath)
		if b, err := json.MarshalIndent(existing, "", "  "); err == nil {
			fmt.Fprintln(os.Stderr, string(b))
		}
		return fmt.Errorf("refusing to overwrite (use --force to replace, or --name <other> to install under a different key)")
	}

	servers[name] = map[string]any{
		"command": exe,
		"args":    []string{"mcp"},
	}
	settings["mcpServers"] = servers

	// Ensure parent dir exists.
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}

	// Write atomically.
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("write settings tmp: %w", err)
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename settings: %w", err)
	}

	fmt.Printf("Installed MCP server '%s' in %s\n", name, settingsPath)
	fmt.Printf("  command: %s mcp\n", exe)
	fmt.Println("Restart Claude Code to pick up the new server.")
	return nil
}
