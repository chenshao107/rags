package project

import (
	"fmt"
	"os"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/vectordb"
)

// ReclaimResourcesForPath removes Qdrant collections tied to a project path.
// Called before init when .rags was removed but vectors may still exist.
func ReclaimResourcesForPath(absPath string, defaultCollection string, vdb *vectordb.Client) error {
	registry, err := config.LoadRegistry()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	toDelete := make(map[string]bool)
	var toUnregister []string

	for _, p := range registry.Projects {
		if p.Path == absPath {
			if p.Collection != "" {
				toDelete[p.Collection] = true
			}
			toUnregister = append(toUnregister, p.Name)
		}
	}
	if defaultCollection != "" {
		toDelete[defaultCollection] = true
	}

	if len(toDelete) == 0 {
		return nil
	}

	for name := range toDelete {
		if err := vdb.DeleteCollection(name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete collection %s: %v\n", name, err)
		}
	}

	if len(toUnregister) > 0 {
		if err := config.UnregisterProjects(toUnregister); err != nil {
			return fmt.Errorf("unregister reclaimed projects: %w", err)
		}
	}

	return nil
}

// NeedsFullReindexReset reports whether the index should be wiped before indexing.
// True when state is empty but the scan found files — e.g. state.json was deleted.
func NeedsFullReindexReset(state config.ProjectState, scannedCount, toIndexCount int) bool {
	return len(state.Files) == 0 && scannedCount > 0 && toIndexCount == scannedCount
}

// ResetCollection deletes a Qdrant collection if it exists (404 is OK).
func ResetCollection(vdb *vectordb.Client, collection string) error {
	if collection == "" {
		return nil
	}
	if err := vdb.DeleteCollection(collection); err != nil {
		return fmt.Errorf("reset collection %s: %w", collection, err)
	}
	return nil
}
