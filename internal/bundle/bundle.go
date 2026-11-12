package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenshao/rags/internal/config"
	"gopkg.in/yaml.v3"
)

const FormatVersion = 2

const (
	entryManifest = "manifest.json"
	entryConfig   = ".rags/config.yaml"
	entryState    = ".rags/state.json"
	entrySnapshot = "snapshot.snapshot"

	maxManifestBytes = 1 << 20
	maxMetaBytes     = 256 << 20
)

// Manifest describes a portable index. It records the embedding identity and
// the collection, and it does not record API keys or machine paths.
type Manifest struct {
	Format      int           `json:"format"`
	ExportedAt  time.Time     `json:"exported_at"`
	ProjectName string        `json:"project_name"`
	Collection  string        `json:"collection"`
	PointsCount uint64        `json:"points_count"`
	VectorSize  int           `json:"vector_size"`
	Distance    string        `json:"distance"`
	SnapshotSHA string        `json:"snapshot_sha256"`
	FileCount   int           `json:"file_count"`
	Embedding   EmbeddingInfo `json:"embedding"`
}

// EmbeddingInfo is the model that produced the stored vectors.
type EmbeddingInfo struct {
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

// Contents is a validated bundle loaded from disk.
type Contents struct {
	Manifest   Manifest
	ConfigYAML []byte
	StateJSON  []byte
	Config     config.ProjectConfig
	State      config.ProjectState
	Snapshot   string // temporary file; caller removes it
}

// DefaultBundleFile returns a file name for a project export.
func DefaultBundleFile(project string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, project)
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "rags"
	}
	return name + ".ragsbundle"
}

func (m Manifest) validate() error {
	if m.Format != FormatVersion {
		return fmt.Errorf("unsupported bundle format %d", m.Format)
	}
	if m.ProjectName == "" {
		return fmt.Errorf("bundle is missing project_name")
	}
	if m.Collection == "" {
		return fmt.Errorf("bundle is missing collection")
	}
	if m.VectorSize <= 0 {
		return fmt.Errorf("bundle is missing vector_size")
	}
	if m.SnapshotSHA == "" {
		return fmt.Errorf("bundle is missing snapshot_sha256")
	}
	if m.Embedding.Model == "" {
		return fmt.Errorf("bundle is missing embedding model")
	}
	if m.FileCount <= 0 {
		return fmt.Errorf("bundle contains no source files")
	}
	if m.Embedding.Dimensions != 0 && m.Embedding.Dimensions != m.VectorSize {
		return fmt.Errorf("bundle embedding dimensions %d do not match vector size %d", m.Embedding.Dimensions, m.VectorSize)
	}
	return nil
}

// CheckModelCompatible reports whether local settings embed queries in the
// same space as the bundle. Missing settings are allowed; import still
// restores vectors, and grep needs the model later.
func CheckModelCompatible(local config.GlobalSettings, settingsPresent bool, m Manifest) error {
	if !settingsPresent {
		return nil
	}
	localSize := config.EmbeddingVectorSize(local.Embedding)
	if local.Embedding.Model != m.Embedding.Model || localSize != m.VectorSize {
		return fmt.Errorf("embedding model mismatch: bundle is %s (%d dims), local settings are %s (%d dims). Search embeds the query with the local model, so results would not match these vectors. Align ~/.rags/settings.yaml with the bundle, or pass --force",
			m.Embedding.Model, m.VectorSize, local.Embedding.Model, localSize)
	}
	return nil
}

// AbsoluteConfigPaths returns path entries that will not follow the project
// directory onto another machine.
func AbsoluteConfigPaths(cfg config.ProjectConfig) []string {
	var abs []string
	for _, p := range cfg.Paths {
		if filepath.IsAbs(p) {
			abs = append(abs, p)
		}
	}
	return abs
}

func writeBundle(dest string, m Manifest, configYAML, stateJSON []byte, snapshotPath string, files []projectFile) error {
	if err := m.validate(); err != nil {
		return err
	}
	partial := dest + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create bundle: %w", err)
	}
	success := false
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
		if !success {
			os.Remove(partial)
		}
	}()

	tw := tar.NewWriter(f)
	manifestJSON, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	manifestJSON = append(manifestJSON, '\n')
	if err := writeTarBytes(tw, entryManifest, manifestJSON); err != nil {
		return err
	}
	if err := writeTarFile(tw, entrySnapshot, snapshotPath); err != nil {
		return err
	}
	if err := writeTarBytes(tw, filesPrefix+".rags/config.yaml", configYAML); err != nil {
		return err
	}
	if err := writeTarBytes(tw, filesPrefix+".rags/state.json", stateJSON); err != nil {
		return err
	}
	for _, file := range files {
		if err := writeProjectFile(tw, file); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close bundle: %w", err)
	}
	closed = true
	if err := os.Rename(partial, dest); err != nil {
		os.Remove(partial)
		return fmt.Errorf("rename bundle: %w", err)
	}
	success = true
	return nil
}

func writeTarBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0644,
		Size:    int64(len(data)),
		ModTime: time.Unix(0, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat snapshot: %w", err)
	}
	hdr := &tar.Header{
		Name:    name,
		Mode:    0644,
		Size:    info.Size(),
		ModTime: time.Unix(0, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// Open reads a bundle and checks the snapshot hash. Project files are discarded.
// The caller must remove Contents.Snapshot.
func Open(bundlePath string) (Contents, error) {
	return readBundle(bundlePath, "", "", nil)
}

// readBundle checks a bundle. When dest is set, project files are extracted there.
// gate runs after the manifest is read and before any project file is written.
func readBundle(bundlePath, dest, bundleAbs string, gate func(Manifest) error) (Contents, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return Contents{}, fmt.Errorf("open bundle: %w", err)
	}
	defer f.Close()

	var (
		manifestJSON []byte
		configYAML   []byte
		stateJSON    []byte
		seen         = map[string]bool{}
		snapshotTmp  string
		snapshotSHA  string
		fileCount    int
		gated        bool
	)
	cleanup := func() {
		if snapshotTmp != "" {
			os.Remove(snapshotTmp)
		}
	}
	fail := func(err error) (Contents, error) {
		cleanup()
		return Contents{}, err
	}

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(fmt.Errorf("read bundle: %w", err))
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA && hdr.Typeflag != tar.TypeDir && hdr.Typeflag != '\x00' {
			continue
		}
		name, err := cleanEntryName(strings.TrimSuffix(hdr.Name, "/"))
		if err != nil {
			return fail(err)
		}
		if !gated {
			if name != entryManifest {
				return fail(fmt.Errorf("bundle must start with %s", entryManifest))
			}
		}

		rel, isProject := projectRel(name)
		switch {
		case name == entryManifest || name == entrySnapshot || isProject && isMetadataRel(rel):
			if seen[name] {
				return fail(fmt.Errorf("duplicate bundle entry %s", name))
			}
			seen[name] = true
		case isProject:
		default:
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return fail(fmt.Errorf("skip %s: %w", name, err))
			}
			continue
		}

		switch {
		case name == entryManifest:
			manifestJSON, err = readLimited(tr, maxManifestBytes)
			if err != nil {
				return fail(err)
			}
			var manifest Manifest
			if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
				return fail(fmt.Errorf("parse manifest: %w", err))
			}
			if err := manifest.validate(); err != nil {
				return fail(err)
			}
			if gate != nil {
				if err := gate(manifest); err != nil {
					return fail(err)
				}
			}
			gated = true
		case name == entrySnapshot:
			snapshotTmp, snapshotSHA, err = extractSnapshot(tr)
		case isProject && isMetadataRel(rel):
			var data []byte
			data, err = readLimited(tr, maxMetaBytes)
			if err != nil {
				return fail(err)
			}
			if rel == ".rags/config.yaml" {
				configYAML = data
			} else {
				stateJSON = data
			}
			if dest != "" {
				err = writeExtracted(dest, bundleAbs, rel, hdr, bytes.NewReader(data))
			}
		case isProject:
			if dest == "" || hdr.Typeflag == tar.TypeDir {
				if hdr.Typeflag != tar.TypeDir {
					_, err = io.Copy(io.Discard, tr)
				}
				if err == nil && dest != "" && hdr.Typeflag == tar.TypeDir {
					err = writeExtracted(dest, bundleAbs, rel, hdr, nil)
				}
			} else {
				err = writeExtracted(dest, bundleAbs, rel, hdr, tr)
			}
			if err == nil && hdr.Typeflag != tar.TypeDir {
				fileCount++
			}
		}
		if err != nil {
			return fail(err)
		}
	}

	for _, name := range []string{entryManifest, entrySnapshot, filesPrefix + ".rags/config.yaml", filesPrefix + ".rags/state.json"} {
		if !seen[name] {
			return fail(fmt.Errorf("bundle is missing %s", name))
		}
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		cleanup()
		return Contents{}, fmt.Errorf("parse manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		cleanup()
		return Contents{}, err
	}
	if !strings.EqualFold(manifest.SnapshotSHA, snapshotSHA) {
		cleanup()
		return Contents{}, fmt.Errorf("snapshot checksum mismatch")
	}
	if fileCount != manifest.FileCount {
		cleanup()
		return Contents{}, fmt.Errorf("bundle lists %d source files, found %d", manifest.FileCount, fileCount)
	}

	cfg, state, err := parseProjectFiles(configYAML, stateJSON)
	if err != nil {
		cleanup()
		return Contents{}, err
	}
	if cfg.Name != "" && cfg.Name != manifest.ProjectName {
		cleanup()
		return Contents{}, fmt.Errorf("manifest project %q does not match .rags config %q", manifest.ProjectName, cfg.Name)
	}
	if cfg.Collection != "" && cfg.Collection != manifest.Collection {
		cleanup()
		return Contents{}, fmt.Errorf("manifest collection %q does not match .rags config %q", manifest.Collection, cfg.Collection)
	}
	cfg.Name = manifest.ProjectName
	cfg.Collection = manifest.Collection

	return Contents{
		Manifest:   manifest,
		ConfigYAML: configYAML,
		StateJSON:  stateJSON,
		Config:     cfg,
		State:      state,
		Snapshot:   snapshotTmp,
	}, nil
}

func writeExtracted(dest, bundleAbs, rel string, hdr *tar.Header, r io.Reader) error {
	target, err := safeJoin(dest, rel)
	if err != nil {
		return err
	}
	if bundleAbs != "" && filepath.Clean(target) == filepath.Clean(bundleAbs) {
		return fmt.Errorf("refusing to overwrite the bundle at %s; choose a different --path", bundleAbs)
	}
	if hdr.Typeflag == tar.TypeDir {
		if err := os.MkdirAll(target, 0755); err != nil {
			return fmt.Errorf("create %s: %w", rel, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create directory for %s: %w", rel, err)
	}
	mode := os.FileMode(hdr.Mode) & 0777
	if mode == 0 {
		mode = 0644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	defer out.Close()
	if r != nil {
		if _, err := io.Copy(out, r); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

func parseProjectFiles(configYAML, stateJSON []byte) (config.ProjectConfig, config.ProjectState, error) {
	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(configYAML, &cfg); err != nil {
		return config.ProjectConfig{}, config.ProjectState{}, fmt.Errorf("parse .rags/config.yaml: %w", err)
	}
	if cfg.IsPublic() {
		return config.ProjectConfig{}, config.ProjectState{}, fmt.Errorf("bundle is a public repository; import only restores a private project")
	}
	if cfg.Mode == "" {
		cfg.Mode = config.ModePrivate
	}
	var state config.ProjectState
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		return config.ProjectConfig{}, config.ProjectState{}, fmt.Errorf("parse .rags/state.json: %w", err)
	}
	if len(state.Files) == 0 {
		return config.ProjectConfig{}, config.ProjectState{}, fmt.Errorf(".rags/state.json has no files")
	}
	return cfg, state, nil
}

func cleanEntryName(name string) (string, error) {
	cleaned := path.Clean(name)
	if cleaned != name || cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid bundle entry %q", name)
	}
	return cleaned, nil
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("bundle entry exceeds %d bytes", max)
	}
	return b, nil
}

func extractSnapshot(r io.Reader) (string, string, error) {
	f, err := os.CreateTemp("", "rags-import-*.snapshot")
	if err != nil {
		return "", "", fmt.Errorf("create temp snapshot: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", fmt.Errorf("extract snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", fmt.Errorf("close temp snapshot: %w", err)
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}
