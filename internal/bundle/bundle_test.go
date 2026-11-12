package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/vectordb"
)

func TestRebindRegistry(t *testing.T) {
	entry := config.ProjectEntry{Name: "docs", Path: "/new/docs", Collection: "rags_docs_aaa"}
	reg := config.ProjectRegistry{Projects: []config.ProjectEntry{
		{Name: "docs", Path: "/old/docs", Collection: "rags_docs_aaa"},
		{Name: "other", Path: "/keep", Collection: "rags_other"},
	}}

	got, err := RebindRegistry(reg, entry, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Registry.Projects) != 2 {
		t.Fatalf("projects: %#v", got.Registry.Projects)
	}
	if got.Registry.Projects[0].Name != "other" {
		t.Fatalf("dropped an unrelated project: %#v", got.Registry.Projects)
	}
	if got.Registry.Projects[1].Path != "/new/docs" {
		t.Fatalf("new path missing: %#v", got.Registry.Projects[1])
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Path != "/old/docs" {
		t.Fatalf("dropped: %#v", got.Dropped)
	}

	conflict := config.ProjectRegistry{Projects: []config.ProjectEntry{
		{Name: "docs", Path: "/elsewhere", Collection: "rags_other_collection"},
	}}
	if _, err := RebindRegistry(conflict, entry, false); err == nil {
		t.Fatal("expected name conflict")
	}
	forced, err := RebindRegistry(conflict, entry, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(forced.Registry.Projects) != 1 || forced.Registry.Projects[0].Collection != entry.Collection {
		t.Fatalf("force replace: %#v", forced.Registry.Projects)
	}
}

func TestCheckModelCompatible(t *testing.T) {
	manifest := Manifest{
		Embedding:  EmbeddingInfo{Model: "Qwen/Qwen3-Embedding-8B", Dimensions: 4096},
		VectorSize: 4096,
	}
	match := config.GlobalSettings{Embedding: config.EmbeddingConfig{
		Model:      "Qwen/Qwen3-Embedding-8B",
		Dimensions: 4096,
	}}
	if err := CheckModelCompatible(match, true, manifest); err != nil {
		t.Fatal(err)
	}
	if err := CheckModelCompatible(config.DefaultSettings(), false, manifest); err != nil {
		t.Fatal(err)
	}
	other := match
	other.Embedding.Model = "BAAI/bge-m3"
	other.Embedding.Dimensions = 1024
	if err := CheckModelCompatible(other, true, manifest); err == nil {
		t.Fatal("expected model mismatch")
	}
}

func TestOpenRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bad.ragsbundle")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeTarBytes(tw, "../evil", []byte("nope")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bundlePath); err == nil {
		t.Fatal("expected invalid entry error")
	}
}

func TestExportRejectsPublicProject(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultProjectConfig("pub")
	cfg.Mode = config.ModePublic
	cfg.Public = &config.PublicMeta{Collection: "rags_pub"}
	if err := config.SaveProjectConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	_, err := Export(vectordb.NewClient("http://127.0.0.1:1"), config.DefaultSettings(), ExportOptions{
		ProjectRoot: dir,
		OutputPath:  filepath.Join(dir, "out.ragsbundle"),
	})
	if err == nil || !strings.Contains(err.Error(), "public") {
		t.Fatalf("expected public rejection, got %v", err)
	}
}

func TestBundleRoundTripOmitsSecretsAndPaths(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "novel")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultProjectConfig("novel")
	cfg.Collection = "rags_novel_abc"
	if err := config.SaveProjectConfig(project, cfg); err != nil {
		t.Fatal(err)
	}
	chapter := "chapter one"
	if err := os.MkdirAll(filepath.Join(project, "chapters"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "chapters", "01.txt"), []byte(chapter), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "extra.bin"), []byte("not-indexed"), 0644); err != nil {
		t.Fatal(err)
	}
	state := config.ProjectState{Files: map[string]string{"chapters/01.txt": sha256Hex(chapter)}}
	if err := config.SaveProjectState(project, state); err != nil {
		t.Fatal(err)
	}

	const snapshotBody = "qdrant-snapshot-body"
	var deleted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections/rags_novel_abc":
			_, _ = w.Write([]byte(`{"result":{"points_count":12,"config":{"params":{"vectors":{"size":4096,"distance":"Cosine"}}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/collections/rags_novel_abc/snapshots":
			_, _ = w.Write([]byte(`{"result":{"name":"rags_novel_abc.snapshot","size":20},"status":"ok"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/collections/rags_novel_abc/snapshots/rags_novel_abc.snapshot":
			_, _ = w.Write([]byte(snapshotBody))
		case r.Method == http.MethodDelete && r.URL.Path == "/collections/rags_novel_abc/snapshots/rags_novel_abc.snapshot":
			deleted = true
			_, _ = w.Write([]byte(`{"result":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	settings := config.GlobalSettings{
		Qdrant: config.QdrantConfig{URL: srv.URL},
		Embedding: config.EmbeddingConfig{
			Provider:   "siliconflow",
			Model:      "Qwen/Qwen3-Embedding-8B",
			Dimensions: 4096,
			APIKey:     "super-secret-key",
		},
	}
	output := filepath.Join(dir, "novel.ragsbundle")
	var log bytes.Buffer
	result, err := Export(vectordb.NewClient(srv.URL), settings, ExportOptions{
		ProjectRoot: project,
		OutputPath:  output,
		Stdout:      &log,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("server snapshot was not deleted")
	}
	if result.PointsCount != 12 || result.Files != 2 {
		t.Fatalf("result: %+v", result)
	}

	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-key") || strings.Contains(string(raw), project) {
		t.Fatal("bundle contains an API key or absolute project path")
	}

	restored := filepath.Join(dir, "restored")
	contents, err := readBundle(output, restored, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(contents.Snapshot)
	gotChapter, err := os.ReadFile(filepath.Join(restored, "chapters", "01.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotChapter) != chapter {
		t.Fatalf("chapter: %q", gotChapter)
	}
	if _, err := os.Stat(filepath.Join(restored, "empty")); err != nil {
		t.Fatal(err)
	}
	gotExtra, err := os.ReadFile(filepath.Join(restored, "extra.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotExtra) != "not-indexed" {
		t.Fatalf("extra: %q", gotExtra)
	}
	if _, err := os.Stat(filepath.Join(restored, ".rags", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	gotSnap, err := os.ReadFile(contents.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSnap) != snapshotBody {
		t.Fatalf("snapshot: %q", gotSnap)
	}
	if contents.Manifest.Collection != "rags_novel_abc" || contents.Manifest.Embedding.Model != settings.Embedding.Model {
		t.Fatalf("manifest: %+v", contents.Manifest)
	}
	if contents.Config.Collection != "rags_novel_abc" {
		t.Fatalf("config collection: %s", contents.Config.Collection)
	}
	if contents.State.Files["chapters/01.txt"] != sha256Hex(chapter) {
		t.Fatalf("state: %#v", contents.State.Files)
	}
}

func TestImportRestoresCollectionAndRebindsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	project := filepath.Join(dir, "source")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultProjectConfig("source")
	cfg.Collection = "rags_source_abc"
	if err := config.SaveProjectConfig(project, cfg); err != nil {
		t.Fatal(err)
	}
	const body = "original file"
	if err := os.WriteFile(filepath.Join(project, "a.txt"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectState(project, config.ProjectState{Files: map[string]string{"a.txt": sha256Hex(body)}}); err != nil {
		t.Fatal(err)
	}
	if err := config.RegisterProject(config.ProjectEntry{
		Name: "source", Path: project, Collection: cfg.Collection,
	}); err != nil {
		t.Fatal(err)
	}

	const snapshotBody = "imported-snapshot"
	exported := false
	var uploaded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections/rags_source_abc":
			if exported {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"result":{"points_count":3,"config":{"params":{"vectors":{"size":4,"distance":"Cosine"}}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/collections/rags_source_abc/snapshots":
			_, _ = w.Write([]byte(`{"result":{"name":"snap.snapshot","size":3}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/collections/rags_source_abc/snapshots/"):
			_, _ = w.Write([]byte(snapshotBody))
		case r.Method == http.MethodDelete:
			exported = true
			_, _ = w.Write([]byte(`{"result":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/collections/rags_source_abc/snapshots/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			uploaded = r.URL.Query().Get("checksum")
			_, _ = w.Write([]byte(`{"result":true}`))
		case r.Method == http.MethodPut && r.URL.Path == "/collections/rags_source_abc/index":
			_, _ = w.Write([]byte(`{"result":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	settings := config.GlobalSettings{
		Qdrant: config.QdrantConfig{URL: srv.URL},
		Embedding: config.EmbeddingConfig{
			Provider:   "siliconflow",
			Model:      "test-model",
			Dimensions: 4,
			APIKey:     "super-secret-key",
		},
	}
	bundlePath := filepath.Join(dir, "source.ragsbundle")
	if _, err := Export(vectordb.NewClient(srv.URL), settings, ExportOptions{
		ProjectRoot: project,
		OutputPath:  bundlePath,
		Stdout:      io.Discard,
	}); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "moved")
	occupied := filepath.Join(dir, "occupied")
	if err := os.MkdirAll(occupied, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "keep.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(vectordb.NewClient(srv.URL), settings, ImportOptions{
		BundlePath:      bundlePath,
		DestPath:        occupied,
		SettingsPresent: true,
		Stdout:          io.Discard,
	}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("expected non-empty destination error, got %v", err)
	}

	var log bytes.Buffer
	result, err := Import(vectordb.NewClient(srv.URL), settings, ImportOptions{
		BundlePath:      bundlePath,
		DestPath:        dest,
		SettingsPresent: true,
		Stdout:          &log,
	})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded == "" {
		t.Fatal("snapshot was not uploaded")
	}
	if result.ProjectRoot != dest || result.Collection != "rags_source_abc" || result.Files != 1 {
		t.Fatalf("result: %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("restored file: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, ".rags", "state.json")); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadProjectConfig(dest)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Collection != "rags_source_abc" || loaded.Mode != config.ModePrivate {
		t.Fatalf("imported config: %+v", loaded)
	}
	restored, err := config.LoadProjectState(dest)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Files["a.txt"] != sha256Hex(body) {
		t.Fatalf("state: %#v", restored.Files)
	}

	reg, err := config.LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Projects) != 1 {
		t.Fatalf("registry: %#v", reg.Projects)
	}
	if reg.Projects[0].Path != dest || reg.Projects[0].Collection != "rags_source_abc" {
		t.Fatalf("registry entry: %+v", reg.Projects[0])
	}
	if !strings.Contains(log.String(), "no longer points") {
		t.Fatalf("expected old path to be unbound, log:\n%s", log.String())
	}

	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-key") || strings.Contains(string(raw), project) {
		t.Fatal("bundle contains an API key or absolute project path")
	}
}

func TestExportRequiresIndexedFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultProjectConfig("docs")
	if err := config.SaveProjectConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProjectState(dir, config.ProjectState{Files: map[string]string{"missing.txt": "abc"}}); err != nil {
		t.Fatal(err)
	}
	_, err := Export(vectordb.NewClient("http://127.0.0.1:1"), config.DefaultSettings(), ExportOptions{
		ProjectRoot: dir,
		OutputPath:  filepath.Join(dir, "out.ragsbundle"),
	})
	if err == nil || !strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("expected missing file error, got %v", err)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
