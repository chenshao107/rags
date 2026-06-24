package project

import (
	"fmt"
	"os"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/vectordb"
)

// GarbageCollect checks all registered projects and cleans up those whose
// directories no longer exist. Returns a list of cleaned project names.
func GarbageCollect(vdb *vectordb.Client) ([]string, error) {
	registry, err := config.LoadRegistry()
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	var cleaned []string
	for _, p := range registry.Projects {
		if !dirExists(p.Path) {
			// Project directory gone — clean up its Qdrant collection
			if err := vdb.DeleteCollection(p.Collection); err != nil {
				// Log but don't fail — collection may already be gone
				fmt.Fprintf(os.Stderr, "Warning: failed to delete collection %s: %v\n", p.Collection, err)
			}
			cleaned = append(cleaned, p.Name)
		}
	}

	if len(cleaned) > 0 {
		// Remove cleaned projects from registry
		if err := config.UnregisterProjects(cleaned); err != nil {
			return cleaned, fmt.Errorf("unregister cleaned projects: %w", err)
		}
	}

	return cleaned, nil
}

// dirExists checks if a directory exists
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
