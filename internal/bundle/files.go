package bundle

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenshao/rags/internal/config"
)

const filesPrefix = "files/"

// projectFile is one path packed under files/ in the bundle.
type projectFile struct {
	Abs      string
	Rel      string // slash-separated, relative to the project root
	IsDir    bool
	Mode     int64
	Size     int64
	ModTime  time.Time
	WantHash string
}

func collectProjectTree(root string, exclude []string, skipAbs string) ([]projectFile, []string, error) {
	root = filepath.Clean(root)
	skipAbs = filepath.Clean(skipAbs)
	excludeSet := map[string]bool{config.RagsDir: true}
	for _, name := range exclude {
		if name != "" {
			excludeSet[name] = true
		}
	}

	var files []projectFile
	var skipped []string
	err := filepath.Walk(root, func(abs string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		abs = filepath.Clean(abs)
		if abs == root {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if excludeSet[info.Name()] {
				return filepath.SkipDir
			}
			files = append(files, projectFile{
				Abs:     abs,
				Rel:     rel,
				IsDir:   true,
				Mode:    0755,
				ModTime: info.ModTime(),
			})
			return nil
		}
		if excludeSet[info.Name()] || abs == skipAbs {
			return nil
		}
		if !info.Mode().IsRegular() {
			skipped = append(skipped, rel)
			return nil
		}
		mode := int64(info.Mode().Perm())
		if mode == 0 {
			mode = 0644
		}
		files = append(files, projectFile{
			Abs:     abs,
			Rel:     rel,
			Mode:    mode,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk project: %w", err)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Rel < files[j].Rel
	})
	return files, skipped, nil
}

func sourceFiles(files []projectFile) []projectFile {
	var out []projectFile
	for _, f := range files {
		if !f.IsDir {
			out = append(out, f)
		}
	}
	return out
}

func ensureIndexedFiles(state config.ProjectState, files []projectFile) error {
	have := make(map[string]bool, len(files))
	for _, f := range files {
		if !f.IsDir {
			have[f.Rel] = true
		}
	}
	var missing []string
	for p := range state.Files {
		rel := filepath.ToSlash(p)
		if !have[rel] {
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return nil
	}
	shown := missing
	if len(shown) > 8 {
		shown = append(append([]string{}, shown[:8]...), "...")
	}
	return fmt.Errorf("indexed files are not in the project directory (%d): %s", len(missing), strings.Join(shown, ", "))
}

func applyStateHashes(files []projectFile, state config.ProjectState) {
	hashes := make(map[string]string, len(state.Files))
	for p, hash := range state.Files {
		hashes[filepath.ToSlash(p)] = hash
	}
	for i := range files {
		if hash, ok := hashes[files[i].Rel]; ok {
			files[i].WantHash = hash
		}
	}
}

func writeProjectFile(tw *tar.Writer, f projectFile) error {
	name := filesPrefix + f.Rel
	if f.IsDir {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0755,
			Typeflag: tar.TypeDir,
			ModTime:  f.ModTime,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write %s: %w", f.Rel, err)
		}
		return nil
	}

	in, err := os.Open(f.Abs)
	if err != nil {
		return fmt.Errorf("open %s: %w", f.Rel, err)
	}
	defer in.Close()

	hdr := &tar.Header{
		Name:     name,
		Mode:     f.Mode,
		Size:     f.Size,
		Typeflag: tar.TypeReg,
		ModTime:  f.ModTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", f.Rel, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(tw, io.TeeReader(in, hash)); err != nil {
		return fmt.Errorf("write %s: %w", f.Rel, err)
	}
	if f.WantHash != "" && !strings.EqualFold(f.WantHash, hex.EncodeToString(hash.Sum(nil))) {
		return fmt.Errorf("file %s does not match the index hash; run rags update before export", f.Rel)
	}
	return nil
}

func projectRel(name string) (string, bool) {
	if !strings.HasPrefix(name, filesPrefix) {
		return "", false
	}
	rel := strings.TrimPrefix(name, filesPrefix)
	if rel == "" || strings.HasSuffix(name, "/") {
		rel = strings.TrimSuffix(rel, "/")
	}
	return rel, rel != ""
}

func isMetadataRel(rel string) bool {
	return rel == ".rags/config.yaml" || rel == ".rags/state.json"
}

func safeJoin(dest, rel string) (string, error) {
	if rel == "" || rel == "." || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("invalid file path %q", rel)
	}
	full := filepath.Join(dest, filepath.FromSlash(rel))
	back, err := filepath.Rel(dest, full)
	if err != nil {
		return "", err
	}
	if back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid file path %q", rel)
	}
	return full, nil
}
