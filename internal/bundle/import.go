package bundle

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/vectordb"
	"gopkg.in/yaml.v3"
)

// ImportOptions selects the bundle and the directory to recreate.
type ImportOptions struct {
	BundlePath      string
	DestPath        string
	Force           bool
	SettingsPresent bool
	Stdout          io.Writer
}

// ImportResult describes a restored project.
type ImportResult struct {
	ProjectRoot string
	Collection  string
	PointsCount uint64
	Files       int
}

// Import recreates the project directory from a bundle and registers it at the destination path.
// The directory contains the original files and .rags. Vectors are restored unchanged.
// The embedding API is not called.
func Import(vdb *vectordb.Client, settings config.GlobalSettings, opt ImportOptions) (ImportResult, error) {
	out := opt.Stdout
	if out == nil {
		out = io.Discard
	}
	if opt.BundlePath == "" {
		return ImportResult{}, fmt.Errorf("bundle path is required")
	}
	if opt.DestPath == "" {
		return ImportResult{}, fmt.Errorf("destination path is required")
	}
	dest, err := filepath.Abs(opt.DestPath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("resolve destination: %w", err)
	}
	bundleAbs, err := filepath.Abs(opt.BundlePath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("resolve bundle path: %w", err)
	}
	plan, err := planExtract(dest, bundleAbs, opt.Force)
	if err != nil {
		return ImportResult{}, err
	}
	defer plan.abort()

	fmt.Fprintf(out, "Reading %s...\n", opt.BundlePath)
	var (
		rebound RebindResult
		exists  bool
	)
	contents, err := readBundle(opt.BundlePath, plan.staging, bundleAbs, func(manifest Manifest) error {
		if err := CheckModelCompatible(settings, opt.SettingsPresent, manifest); err != nil && !opt.Force {
			return err
		} else if err != nil {
			fmt.Fprintf(out, "Warning: %v\n", err)
		}
		if !opt.SettingsPresent {
			fmt.Fprintf(out, "Warning: no ~/.rags/settings.yaml yet. The directory and vectors will be restored, but rags grep needs this model to embed queries:\n")
			fmt.Fprintf(out, "  model:      %s\n", manifest.Embedding.Model)
			fmt.Fprintf(out, "  dimensions: %d\n", manifest.VectorSize)
		}
		var err error
		exists, err = vdb.CollectionExists(manifest.Collection)
		if err != nil {
			return err
		}
		if exists && !opt.Force {
			return fmt.Errorf("collection %s already exists. Pass --force to replace it with the bundle", manifest.Collection)
		}
		registry, err := config.LoadRegistry()
		if err != nil {
			return fmt.Errorf("load registry: %w", err)
		}
		rebound, err = RebindRegistry(registry, config.ProjectEntry{
			Name:       manifest.ProjectName,
			Path:       dest,
			Collection: manifest.Collection,
		}, opt.Force)
		return err
	})
	if err != nil {
		return ImportResult{}, err
	}
	defer os.Remove(contents.Snapshot)

	for _, p := range AbsoluteConfigPaths(contents.Config) {
		fmt.Fprintf(out, "Warning: .rags/config.yaml path %q is absolute. rags update will keep reading that path.\n", p)
	}
	fmt.Fprintf(out, "Restoring %d files into %s...\n", contents.Manifest.FileCount, dest)
	if exists {
		fmt.Fprintf(out, "Replacing collection %s...\n", contents.Manifest.Collection)
	} else {
		fmt.Fprintf(out, "Restoring collection %s...\n", contents.Manifest.Collection)
	}
	if err := vdb.UploadSnapshot(contents.Manifest.Collection, contents.Snapshot, contents.Manifest.SnapshotSHA); err != nil {
		return ImportResult{}, err
	}
	if err := vdb.EnsurePayloadIndex(contents.Manifest.Collection, "file_path", true); err != nil {
		return ImportResult{}, fmt.Errorf("collection was restored, but the file_path index was not created: %w. Retry with --force", err)
	}

	if err := installProjectFiles(plan.staging, contents); err != nil {
		return ImportResult{}, fmt.Errorf("collection was restored, but .rags was not written: %w. Retry with --force", err)
	}
	if err := plan.commit(); err != nil {
		return ImportResult{}, fmt.Errorf("collection was restored, but the directory was not moved into place: %w", err)
	}
	if err := config.SaveRegistry(rebound.Registry); err != nil {
		return ImportResult{}, fmt.Errorf("the directory and collection were restored, but the project registry was not updated: %w", err)
	}
	for _, old := range rebound.Dropped {
		if old.Path != dest {
			fmt.Fprintf(out, "Registry no longer points %s at %s.\n", old.Collection, old.Path)
		}
	}

	fmt.Fprintf(out, "Imported project %q\n", contents.Manifest.ProjectName)
	fmt.Fprintf(out, "  Collection: %s\n", contents.Manifest.Collection)
	fmt.Fprintf(out, "  Path:       %s\n", dest)
	fmt.Fprintf(out, "  Points:     %d\n", contents.Manifest.PointsCount)
	fmt.Fprintf(out, "  Files:      %d\n", contents.Manifest.FileCount)
	fmt.Fprintf(out, "The directory contains the original files and .rags. Vectors were restored as-is.\n")

	return ImportResult{
		ProjectRoot: dest,
		Collection:  contents.Manifest.Collection,
		PointsCount: contents.Manifest.PointsCount,
		Files:       contents.Manifest.FileCount,
	}, nil
}

type extractPlan struct {
	staging   string
	final     string
	remove    bool
	committed bool
}

func (p *extractPlan) commit() error {
	if !p.remove {
		p.committed = true
		return nil
	}
	if err := os.Rename(p.staging, p.final); err != nil {
		return err
	}
	p.committed = true
	p.remove = false
	return nil
}

func (p *extractPlan) abort() {
	if p == nil || p.committed || !p.remove || p.staging == "" {
		return
	}
	os.RemoveAll(p.staging)
}

func planExtract(dest, bundleAbs string, force bool) (extractPlan, error) {
	if filepath.Clean(dest) == filepath.Clean(bundleAbs) {
		return extractPlan{}, fmt.Errorf("destination is the bundle file; pass --path for a directory")
	}
	info, err := os.Stat(dest)
	if err != nil {
		if !os.IsNotExist(err) {
			return extractPlan{}, fmt.Errorf("destination: %w", err)
		}
		parent := filepath.Dir(dest)
		if err := os.MkdirAll(parent, 0755); err != nil {
			return extractPlan{}, fmt.Errorf("create destination parent: %w", err)
		}
		staging, err := os.MkdirTemp(parent, ".rags-import-*")
		if err != nil {
			return extractPlan{}, fmt.Errorf("create destination: %w", err)
		}
		return extractPlan{staging: staging, final: dest, remove: true}, nil
	}
	if !info.IsDir() {
		return extractPlan{}, fmt.Errorf("destination %s is not a directory", dest)
	}
	if config.HasRagsProject(dest) {
		existing, err := config.LoadProjectConfig(dest)
		if err != nil {
			return extractPlan{}, fmt.Errorf("existing project: %w", err)
		}
		if existing.IsPublic() {
			return extractPlan{}, fmt.Errorf("destination is a public repository; import only restores a private project")
		}
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return extractPlan{}, fmt.Errorf("destination: %w", err)
	}
	for _, entry := range entries {
		if filepath.Join(dest, entry.Name()) == bundleAbs {
			continue
		}
		if !force {
			return extractPlan{}, fmt.Errorf("destination %s is not empty (%s). Pass --path for a new directory, or --force to overwrite files", dest, entry.Name())
		}
		break
	}
	return extractPlan{staging: dest}, nil
}

func installProjectFiles(dest string, contents Contents) error {
	cfg := contents.Config
	cfg.Name = contents.Manifest.ProjectName
	cfg.Collection = contents.Manifest.Collection
	cfg.Mode = config.ModePrivate
	cfg.Public = nil
	dir := filepath.Join(dest, config.RagsDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create .rags: %w", err)
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("encode project config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), contents.StateJSON, 0644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// RebindResult is a registry with this project bound to its new path.
type RebindResult struct {
	Registry config.ProjectRegistry
	Dropped  []config.ProjectEntry
}

// RebindRegistry drops stale bindings for this collection or path so a later
// update does not treat the old absolute path as a deleted project and remove
// the collection. A different project that already uses this name is left in
// place unless force is set.
func RebindRegistry(reg config.ProjectRegistry, entry config.ProjectEntry, force bool) (RebindResult, error) {
	if entry.Name == "" || entry.Path == "" || entry.Collection == "" {
		return RebindResult{}, fmt.Errorf("project name, path, and collection are required")
	}
	var kept []config.ProjectEntry
	var dropped []config.ProjectEntry
	for _, p := range reg.Projects {
		samePath := p.Path == entry.Path
		sameName := p.Name == entry.Name
		sameCollection := p.Collection != "" && p.Collection == entry.Collection
		if sameName && !sameCollection && !samePath {
			if !force {
				return RebindResult{}, fmt.Errorf("project name %q is already registered at %s (collection %s). Pass --force to replace that registry entry; the other collection is not deleted", entry.Name, p.Path, p.Collection)
			}
			dropped = append(dropped, p)
			continue
		}
		if samePath || sameCollection {
			dropped = append(dropped, p)
			continue
		}
		kept = append(kept, p)
	}
	kept = append(kept, entry)
	return RebindResult{
		Registry: config.ProjectRegistry{Projects: kept},
		Dropped:  dropped,
	}, nil
}
