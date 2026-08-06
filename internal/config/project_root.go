package config

import (
	"fmt"
	"path/filepath"
)

// FindProjectRoot walks up from startDir until a directory containing .rags is found.
func FindProjectRoot(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	for {
		if HasRagsProject(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not a rags project (no .rags found above %s)", startDir)
		}
		dir = parent
	}
}

// SearchPathPrefix returns the relative path prefix for scoped search based on cwd.
// Empty string means search the entire project (cwd is the project root).
func SearchPathPrefix(projectRoot, cwd string) (string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	cur, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}

	rel, err := filepath.Rel(root, cur)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return "", nil
	}
	return rel, nil
}
