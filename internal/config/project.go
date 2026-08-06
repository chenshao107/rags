package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProjectConfig represents .rags/config.yaml
type ProjectConfig struct {
	Name       string      `yaml:"name"`
	Mode       string      `yaml:"mode,omitempty"`
	Collection string      `yaml:"collection,omitempty"`
	Paths      []string    `yaml:"paths"`
	Exclude []string    `yaml:"exclude"`
	Index   IndexConfig `yaml:"index"`
	Public  *PublicMeta `yaml:"public,omitempty"`
}

type IndexConfig struct {
	ChunkSize int `yaml:"chunk_size"`
	Overlap   int `yaml:"overlap"`
}

// ProjectState represents .rags/state.json (file_path -> file_hash)
type ProjectState struct {
	Files map[string]string `json:"files"`
}

// DefaultProjectConfig returns defaults for a new project
func DefaultProjectConfig(name string) ProjectConfig {
	return ProjectConfig{
		Name:    name,
		Mode:    ModePrivate,
		Paths:   []string{"."},
		Exclude: []string{".git", ".rags", "node_modules", "__pycache__", ".venv", "vendor", ".idea", ".vscode"},
		Index: IndexConfig{
			ChunkSize: 800,
			Overlap:   150,
		},
	}
}

// LoadProjectConfig reads .rags/config.yaml from the given project root
func LoadProjectConfig(projectRoot string) (ProjectConfig, error) {
	path := filepath.Join(projectRoot, RagsDir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectConfig{}, fmt.Errorf("read project config: %w", err)
	}
	var c ProjectConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return ProjectConfig{}, fmt.Errorf("parse project config: %w", err)
	}
	if c.Mode == "" {
		c.Mode = ModePrivate
	}
	return c, nil
}

// SaveProjectConfig writes .rags/config.yaml
func SaveProjectConfig(projectRoot string, c ProjectConfig) error {
	dir := filepath.Join(projectRoot, RagsDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create .rags dir: %w", err)
	}
	data, err := yaml.Marshal(&c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// LoadProjectState reads .rags/state.json
func LoadProjectState(projectRoot string) (ProjectState, error) {
	path := filepath.Join(projectRoot, RagsDir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectState{Files: make(map[string]string)}, nil
		}
		return ProjectState{}, fmt.Errorf("read state: %w", err)
	}
	var s ProjectState
	if err := json.Unmarshal(data, &s); err != nil {
		return ProjectState{Files: make(map[string]string)}, nil
	}
	if s.Files == nil {
		s.Files = make(map[string]string)
	}
	return s, nil
}

// SaveProjectState writes .rags/state.json
func SaveProjectState(projectRoot string, s ProjectState) error {
	dir := filepath.Join(projectRoot, RagsDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create .rags dir: %w", err)
	}
	if s.Files == nil {
		s.Files = make(map[string]string)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// HasRagsProject checks if a directory has an initialized .rags project
func HasRagsProject(dir string) bool {
	path := filepath.Join(dir, RagsDir, "config.yaml")
	_, err := os.Stat(path)
	return err == nil
}
