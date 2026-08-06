package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	RagsDir      = ".rags"
	SettingsFile = "settings.yaml"
	ProjectsFile = "projects.yaml"
)

// GlobalSettings represents ~/.rags/settings.yaml
type GlobalSettings struct {
	Qdrant    QdrantConfig    `yaml:"qdrant"`
	Embedding EmbeddingConfig `yaml:"embedding"`
	Rerank    *RerankConfig   `yaml:"rerank,omitempty"`
}

type QdrantConfig struct {
	URL string `yaml:"url"`
}

type EmbeddingConfig struct {
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
}

type RerankConfig struct {
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
}

// ProjectEntry is a single registered project
type ProjectEntry struct {
	Name       string `yaml:"name"`
	Path       string `yaml:"path"`
	Collection string `yaml:"collection"`
}

// ProjectRegistry represents ~/.rags/projects.yaml
type ProjectRegistry struct {
	Projects []ProjectEntry `yaml:"projects"`
}

// DefaultSettings returns sensible defaults
func DefaultSettings() GlobalSettings {
	return GlobalSettings{
		Qdrant: QdrantConfig{
			URL: "http://localhost:6333",
		},
		Embedding: EmbeddingConfig{
			Provider: "siliconflow",
			BaseURL:  "https://api.siliconflow.cn/v1",
			Model:    "BAAI/bge-m3",
		},
	}
}

// GlobalDir returns the path to ~/.rags
func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".rags"), nil
}

// EnsureGlobalDir creates ~/.rags if it doesn't exist
func EnsureGlobalDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// LoadSettings reads ~/.rags/settings.yaml
func LoadSettings() (GlobalSettings, error) {
	dir, err := GlobalDir()
	if err != nil {
		return DefaultSettings(), err
	}
	return LoadSettingsFromDir(dir)
}

// LoadRootSettings reads /root/.rags/settings.yaml for public server operations.
func LoadRootSettings() (GlobalSettings, error) {
	return LoadSettingsFromDir(filepath.Join(RootHome, RagsDir))
}

// LoadSettingsFromDir reads settings.yaml from a specific directory.
func LoadSettingsFromDir(dir string) (GlobalSettings, error) {
	path := filepath.Join(dir, SettingsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultSettings(), err
	}
	var s GlobalSettings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("parse settings: %w", err)
	}
	return s, nil
}

// SaveSettings writes ~/.rags/settings.yaml
func SaveSettings(s GlobalSettings) error {
	dir, err := EnsureGlobalDir()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(&s)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	path := filepath.Join(dir, SettingsFile)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

// LoadRegistry reads ~/.rags/projects.yaml
func LoadRegistry() (ProjectRegistry, error) {
	dir, err := GlobalDir()
	if err != nil {
		return ProjectRegistry{}, err
	}
	path := filepath.Join(dir, ProjectsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectRegistry{}, nil // empty registry if file doesn't exist
	}
	var r ProjectRegistry
	if err := yaml.Unmarshal(data, &r); err != nil {
		return ProjectRegistry{}, fmt.Errorf("parse projects: %w", err)
	}
	if r.Projects == nil {
		r.Projects = []ProjectEntry{}
	}
	return r, nil
}

// SaveRegistry writes ~/.rags/projects.yaml
func SaveRegistry(r ProjectRegistry) error {
	dir, err := EnsureGlobalDir()
	if err != nil {
		return err
	}
	if r.Projects == nil {
		r.Projects = []ProjectEntry{}
	}
	data, err := yaml.Marshal(&r)
	if err != nil {
		return fmt.Errorf("marshal projects: %w", err)
	}
	path := filepath.Join(dir, ProjectsFile)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write projects: %w", err)
	}
	return nil
}

// RegisterProject adds a project to the registry
func RegisterProject(entry ProjectEntry) error {
	r, err := LoadRegistry()
	if err != nil {
		return err
	}
	// Update if already exists, otherwise append
	found := false
	for i, p := range r.Projects {
		if p.Name == entry.Name || p.Path == entry.Path {
			r.Projects[i] = entry
			found = true
			break
		}
	}
	if !found {
		r.Projects = append(r.Projects, entry)
	}
	return SaveRegistry(r)
}

// UnregisterProject removes a project from the registry by name
func UnregisterProject(name string) error {
	r, err := LoadRegistry()
	if err != nil {
		return err
	}
	filtered := make([]ProjectEntry, 0, len(r.Projects))
	for _, p := range r.Projects {
		if p.Name != name {
			filtered = append(filtered, p)
		}
	}
	r.Projects = filtered
	return SaveRegistry(r)
}

// UnregisterProjects removes multiple projects from the registry by name
func UnregisterProjects(names []string) error {
	r, err := LoadRegistry()
	if err != nil {
		return err
	}
	removeSet := make(map[string]bool, len(names))
	for _, n := range names {
		removeSet[n] = true
	}
	filtered := make([]ProjectEntry, 0, len(r.Projects))
	for _, p := range r.Projects {
		if !removeSet[p.Name] {
			filtered = append(filtered, p)
		}
	}
	r.Projects = filtered
	return SaveRegistry(r)
}
