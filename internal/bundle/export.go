package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/vectordb"
	"gopkg.in/yaml.v3"
)

// ExportOptions selects the project to package and where to write the bundle.
type ExportOptions struct {
	ProjectRoot string
	OutputPath  string
	Force       bool
	Stdout      io.Writer
}

// ExportResult describes the bundle that was written.
type ExportResult struct {
	OutputPath  string
	Collection  string
	PointsCount uint64
	Files       int
	Bytes       int64
}

// Export packages the project directory, its .rags metadata, and the Qdrant
// snapshot. Import recreates that directory without embedding the files again.
func Export(vdb *vectordb.Client, settings config.GlobalSettings, opt ExportOptions) (ExportResult, error) {
	out := opt.Stdout
	if out == nil {
		out = io.Discard
	}
	if opt.ProjectRoot == "" {
		return ExportResult{}, fmt.Errorf("project root is required")
	}
	if opt.OutputPath == "" {
		return ExportResult{}, fmt.Errorf("output path is required")
	}

	pcfg, err := config.LoadProjectConfig(opt.ProjectRoot)
	if err != nil {
		return ExportResult{}, fmt.Errorf("not a rags project (run 'rags init' first): %w", err)
	}
	if pcfg.IsPublic() {
		return ExportResult{}, fmt.Errorf("export does not support public repositories")
	}
	if settings.Embedding.Model == "" {
		return ExportResult{}, fmt.Errorf("embedding model is empty in ~/.rags/settings.yaml")
	}

	collection, err := config.ResolveCollection(opt.ProjectRoot, pcfg)
	if err != nil {
		return ExportResult{}, fmt.Errorf("resolve collection: %w", err)
	}
	if collection == "" {
		return ExportResult{}, fmt.Errorf("project has no collection")
	}

	statePath := filepath.Join(opt.ProjectRoot, config.RagsDir, "state.json")
	stateJSON, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ExportResult{}, fmt.Errorf("no .rags/state.json; run rags update before export")
		}
		return ExportResult{}, fmt.Errorf("read state: %w", err)
	}
	state, err := config.LoadProjectState(opt.ProjectRoot)
	if err != nil {
		return ExportResult{}, err
	}
	if len(state.Files) == 0 {
		return ExportResult{}, fmt.Errorf(".rags/state.json has no files; run rags update before export")
	}

	outputPath, err := filepath.Abs(opt.OutputPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("resolve output path: %w", err)
	}
	if _, err := os.Stat(outputPath); err == nil && !opt.Force {
		return ExportResult{}, fmt.Errorf("%s already exists; pass --force to overwrite", outputPath)
	} else if err != nil && !os.IsNotExist(err) {
		return ExportResult{}, fmt.Errorf("check output path: %w", err)
	}

	tree, skipped, err := collectProjectTree(opt.ProjectRoot, nil, outputPath)
	if err != nil {
		return ExportResult{}, err
	}
	applyStateHashes(tree, state)
	if err := ensureIndexedFiles(state, tree); err != nil {
		return ExportResult{}, err
	}
	sources := sourceFiles(tree)
	if len(sources) == 0 {
		return ExportResult{}, fmt.Errorf("project directory has no files to pack")
	}
	for _, rel := range skipped {
		fmt.Fprintf(out, "Warning: skipped non-regular file %s\n", rel)
	}
	fmt.Fprintf(out, "Packing %d source files...\n", len(sources))

	fmt.Fprintf(out, "Reading collection %s...\n", collection)
	meta, err := vdb.CollectionMeta(collection)
	if err != nil {
		return ExportResult{}, err
	}
	if meta.PointsCount == 0 {
		return ExportResult{}, fmt.Errorf("collection %s has no points", collection)
	}
	if meta.VectorSize <= 0 {
		return ExportResult{}, fmt.Errorf("collection %s has no vector size", collection)
	}
	if configured := config.EmbeddingVectorSize(settings.Embedding); configured != meta.VectorSize {
		fmt.Fprintf(out, "Warning: settings dimensions are %d, but collection %s stores %d-dimensional vectors. The bundle records %d.\n", configured, collection, meta.VectorSize, meta.VectorSize)
	}

	fmt.Fprintf(out, "Creating Qdrant snapshot (%d points)...\n", meta.PointsCount)
	snap, err := vdb.CreateSnapshot(collection)
	if err != nil {
		return ExportResult{}, err
	}

	tmp, err := os.CreateTemp("", "rags-export-*.snapshot")
	if err != nil {
		_ = vdb.DeleteSnapshot(collection, snap.Name)
		return ExportResult{}, fmt.Errorf("create temp snapshot: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	fmt.Fprintf(out, "Downloading snapshot %s...\n", snap.Name)
	hash := sha256.New()
	_, downloadErr := vdb.DownloadSnapshot(collection, snap.Name, io.MultiWriter(tmp, hash))
	closeErr := tmp.Close()
	if err := vdb.DeleteSnapshot(collection, snap.Name); err != nil {
		fmt.Fprintf(out, "Warning: failed to delete Qdrant snapshot %s: %v\n", snap.Name, err)
	}
	if downloadErr != nil {
		return ExportResult{}, downloadErr
	}
	if closeErr != nil {
		return ExportResult{}, fmt.Errorf("close temp snapshot: %w", closeErr)
	}
	sum := hex.EncodeToString(hash.Sum(nil))

	pcfg.Collection = collection
	if pcfg.Mode == "" {
		pcfg.Mode = config.ModePrivate
	}
	configYAML, err := yaml.Marshal(&pcfg)
	if err != nil {
		return ExportResult{}, fmt.Errorf("encode project config: %w", err)
	}

	manifest := Manifest{
		Format:      FormatVersion,
		ExportedAt:  time.Now().UTC(),
		ProjectName: pcfg.Name,
		Collection:  collection,
		PointsCount: meta.PointsCount,
		VectorSize:  meta.VectorSize,
		Distance:    meta.Distance,
		SnapshotSHA: sum,
		Embedding: EmbeddingInfo{
			Provider:   settings.Embedding.Provider,
			Model:      settings.Embedding.Model,
			Dimensions: meta.VectorSize,
		},
		FileCount: len(sources),
	}
	if err := writeBundle(outputPath, manifest, configYAML, stateJSON, tmpName, tree); err != nil {
		return ExportResult{}, err
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("stat bundle: %w", err)
	}
	if insideProject(opt.ProjectRoot, outputPath) {
		fmt.Fprintf(out, "Warning: the bundle is inside the project directory. Move it out before rags update, or update will embed the bundle itself.\n")
	}
	for _, p := range AbsoluteConfigPaths(pcfg) {
		fmt.Fprintf(out, "Warning: .rags/config.yaml path %q is absolute and will not follow this directory onto another machine.\n", p)
	}

	fmt.Fprintf(out, "Exported %s\n", outputPath)
	fmt.Fprintf(out, "  Collection: %s\n", collection)
	fmt.Fprintf(out, "  Points:     %d\n", meta.PointsCount)
	fmt.Fprintf(out, "  Files:      %d\n", len(sources))
	fmt.Fprintf(out, "  Size:       %s\n", formatBytes(info.Size()))
	fmt.Fprintf(out, "The bundle is the project directory plus its index, including the source files and .rags.\n")
	fmt.Fprintf(out, "On the other machine: rags import %s --path /new/directory\n", filepath.Base(outputPath))

	return ExportResult{
		OutputPath:  outputPath,
		Collection:  collection,
		PointsCount: meta.PointsCount,
		Files:       len(sources),
		Bytes:       info.Size(),
	}, nil
}

func insideProject(projectRoot, target string) bool {
	rel, err := filepath.Rel(projectRoot, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	prefix := ".." + string(filepath.Separator)
	return rel != ".." && !strings.HasPrefix(rel, prefix)
}
