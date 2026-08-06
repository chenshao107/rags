package config

import "path/filepath"

// ResolveCollection returns the Qdrant collection for a project.
// Priority: public metadata → config.yaml → registry → path-derived name.
func ResolveCollection(projectRoot string, pcfg ProjectConfig) (string, error) {
	if pcfg.IsPublic() && pcfg.Public != nil && pcfg.Public.Collection != "" {
		return pcfg.Public.Collection, nil
	}
	if pcfg.Collection != "" {
		return pcfg.Collection, nil
	}

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}

	registry, err := LoadRegistry()
	if err == nil {
		for _, p := range registry.Projects {
			if p.Path == absRoot && p.Collection != "" {
				return p.Collection, nil
			}
		}
	}

	return CollectionNameForPath(absRoot, pcfg.Name), nil
}
