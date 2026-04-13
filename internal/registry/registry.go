package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const workspaceDir = ".skep-workspace"

// Agent represents a registered repo.
type Agent struct {
	Path string `json:"path"`
	LLM  string `json:"llm"`
}

// Registry holds all known agents in a workspace.
type Registry struct {
	Agents map[string]*Agent `json:"agents"`
	mu     sync.Mutex
	path   string // path to registry.json
}

// FindWorkspace walks up from startDir looking for .skep-workspace/.
// Returns the workspace root, or "" if not found.
func FindWorkspace(startDir string) string {
	dir := startDir
	for {
		candidate := filepath.Join(dir, workspaceDir)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}
	return ""
}

// InitWorkspace creates .skep-workspace/ at the given path.
func InitWorkspace(workspaceRoot string) error {
	dir := filepath.Join(workspaceRoot, workspaceDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}
	// Create empty registry
	r := &Registry{
		Agents: make(map[string]*Agent),
		path:   filepath.Join(dir, "registry.json"),
	}
	return r.Save()
}

// Load reads the registry from the workspace. Finds workspace by walking up from startDir.
func Load() (*Registry, error) {
	return LoadFrom("")
}

// LoadFrom reads the registry, starting search from the given directory.
// If startDir is empty, uses cwd.
func LoadFrom(startDir string) (*Registry, error) {
	if startDir == "" {
		var err error
		startDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}

	wsRoot := FindWorkspace(startDir)
	if wsRoot == "" {
		// No workspace found — return empty registry
		return &Registry{Agents: make(map[string]*Agent)}, nil
	}

	regPath := filepath.Join(wsRoot, workspaceDir, "registry.json")
	r := &Registry{Agents: make(map[string]*Agent), path: regPath}

	data, err := os.ReadFile(regPath)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}

	if err := json.Unmarshal(data, r); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	if r.Agents == nil {
		r.Agents = make(map[string]*Agent)
	}
	r.path = regPath
	return r, nil
}

// Save writes the registry to disk using atomic rename.
func (r *Registry) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.path == "" {
		return fmt.Errorf("no workspace found — run 'skep index' to set up")
	}

	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create registry dir: %w", err)
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}

	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write registry tmp: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename registry: %w", err)
	}
	return nil
}

// Register adds or updates an agent in the registry.
func (r *Registry) Register(name string, agent *Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Agents[name] = agent
}

// Unregister removes an agent from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Agents, name)
}

// Get returns an agent by name.
func (r *Registry) Get(name string) *Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Agents[name]
}

// WorkspaceRoot returns the workspace root directory, or "" if no workspace.
func (r *Registry) WorkspaceRoot() string {
	if r.path == "" {
		return ""
	}
	// path is <workspace>/.skep-workspace/registry.json
	return filepath.Dir(filepath.Dir(r.path))
}
