package publicrepo

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/systemd"
	"github.com/chenshao/rags/internal/vectordb"
)

// InitOptions controls public repository initialization.
type InitOptions struct {
	Name       string
	SourcePath string
	Settings   config.GlobalSettings
	VDB        *vectordb.Client
	IndexFn    func(repoRoot string, pcfg config.ProjectConfig, collection string, settings *config.GlobalSettings, vdb *vectordb.Client) error
}

// Init creates a public repository under /srv/rags/<name>, writes .rags metadata,
// registers the project, installs systemd service, and performs the first index.
func Init(opts InitOptions) (string, error) {
	if !serverIsRoot() {
		return "", fmt.Errorf("public repository init requires root; run: sudo rags init --public [--name NAME] [PATH]")
	}

	sourcePath, err := filepath.Abs(opts.SourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}

	name := opts.Name
	if name == "" {
		name = config.DefaultPublicName(sourcePath)
	}
	if err := config.ValidatePublicName(name); err != nil {
		return "", err
	}

	repoRoot := config.PublicRepoPath(name)
	if config.HasRagsProject(repoRoot) {
		return "", fmt.Errorf("public repository already initialized: %s", repoRoot)
	}
	if stat, err := os.Stat(repoRoot); err == nil && !stat.IsDir() {
		return "", fmt.Errorf("public repository path exists and is not a directory: %s", repoRoot)
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(repoRoot, 0755); err != nil {
			return "", fmt.Errorf("create repo root: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("check repo path: %w", err)
	}

	docsRoot := repoRoot
	paths := []string{"."}
	if sourcePath != repoRoot {
		linkPath := filepath.Join(repoRoot, "docs")
		if err := os.Symlink(sourcePath, linkPath); err != nil {
			return "", fmt.Errorf("link docs from %s: %w", sourcePath, err)
		}
		docsRoot = linkPath
		paths = []string{"./docs"}
	}

	collectionName := config.CollectionName(name)
	settingsDir := filepath.Join(config.RootHome, config.RagsDir)

	pcfg := config.DefaultProjectConfig(name)
	pcfg.Mode = config.ModePublic
	pcfg.Paths = paths
	meta := config.BuildPublicMeta(name, repoRoot, docsRoot, collectionName, settingsDir, config.PublicEndpoint(name), config.DeploySystemd)
	pcfg.Public = &meta

	if err := opts.VDB.EnsureCollection(collectionName, 1024); err != nil {
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

	fmt.Printf("✓ Public repository '%s' initialized\n", name)
	fmt.Printf("  Repo root:  %s\n", repoRoot)
	fmt.Printf("  Docs root:  %s\n", docsRoot)
	fmt.Printf("  Collection: %s\n", collectionName)
	fmt.Printf("  Endpoint:   %s\n", meta.Endpoint)
	fmt.Printf("  Service:    %s\n", meta.Service)

	if opts.IndexFn != nil {
		fmt.Println("Indexing files...")
		if err := opts.IndexFn(repoRoot, pcfg, collectionName, &opts.Settings, opts.VDB); err != nil {
			return "", fmt.Errorf("initial index: %w", err)
		}
	}

	fmt.Println("Installing systemd service...")
	if err := systemd.InstallPublicService(name); err != nil {
		return "", fmt.Errorf("install systemd service: %w", err)
	}

	fmt.Println("✓ Public rags service started")
	fmt.Printf("Users can search with: cd %s && rags grep \"your query\"\n", repoRoot)
	return repoRoot, nil
}

func serverIsRoot() bool {
	return os.Geteuid() == 0
}
