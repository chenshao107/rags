package publicrepo

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/vectordb"
)

// DockerInitOptions controls docker-compose repository initialization.
type DockerInitOptions struct {
	Name        string
	DocsPath    string
	PublicHost  string
	Port        int
	Settings    config.GlobalSettings
	SettingsDir string
	VDB         *vectordb.Client
}

// InitDocker creates a public repository for Docker Compose deployments.
// The docs directory is the project root: .rags lives alongside the documents
// so host users can cd into the docs tree and search with directory scope.
func InitDocker(opts DockerInitOptions) (string, error) {
	name := opts.Name
	if name == "" {
		return "", fmt.Errorf("RAGS_NAME is required")
	}
	if err := config.ValidatePublicName(name); err != nil {
		return "", err
	}
	if opts.Port <= 0 {
		return "", fmt.Errorf("RAGS_PORT must be > 0")
	}

	docsPath := opts.DocsPath
	if docsPath == "" {
		docsPath = config.DockerDocsRoot
	}
	docsPath, err := filepath.Abs(docsPath)
	if err != nil {
		return "", fmt.Errorf("resolve docs path: %w", err)
	}
	if _, err := os.Stat(docsPath); err != nil {
		return "", fmt.Errorf("docs path not accessible: %w", err)
	}

	settingsDir := opts.SettingsDir
	if settingsDir == "" {
		settingsDir = config.DockerSettingsDir
	}

	repoRoot := docsPath

	collectionName := config.CollectionName(name)
	endpoint := config.HTTPEndpoint(opts.PublicHost, opts.Port)

	docsRoot := docsPath
	paths := []string{"."}

	pcfg := config.DefaultProjectConfig(name)
	pcfg.Mode = config.ModePublic
	pcfg.Paths = paths
	meta := config.BuildPublicMeta(name, repoRoot, docsRoot, collectionName, settingsDir, endpoint, config.DeployDocker)
	pcfg.Public = &meta

	alreadyInit := config.HasRagsProject(repoRoot)
	if !alreadyInit {
		if err := opts.VDB.EnsureCollection(collectionName, config.EmbeddingVectorSize(opts.Settings.Embedding)); err != nil {
			return "", fmt.Errorf("create collection: %w", err)
		}
		if err := config.SaveProjectConfig(repoRoot, pcfg); err != nil {
			return "", fmt.Errorf("save project config: %w", err)
		}
		if err := config.RegisterProject(config.ProjectEntry{
			Name:       name,
			Path:       repoRoot,
			Collection: collectionName,
		}); err != nil {
			return "", fmt.Errorf("register project: %w", err)
		}
		fmt.Printf("✓ Docker public repository '%s' initialized\n", name)
	} else {
		existing, err := config.LoadProjectConfig(repoRoot)
		if err != nil {
			return "", fmt.Errorf("load existing config: %w", err)
		}
		pcfg = existing
		fmt.Printf("✓ Docker public repository '%s' already initialized\n", name)
	}

	fmt.Printf("  Repo root:  %s\n", repoRoot)
	fmt.Printf("  Docs root:  %s\n", docsRoot)
	fmt.Printf("  Collection: %s\n", collectionName)
	fmt.Printf("  Endpoint:   %s\n", endpoint)

	if !alreadyInit {
		fmt.Println()
		fmt.Println("Edit .rags/config.yaml and docker/.env as needed, then run:")
		fmt.Println("  ./update.sh")
	}

	fmt.Printf("After ./update.sh, users can search: cd %s && rags grep \"your query\"\n", repoRoot)
	return repoRoot, nil
}
