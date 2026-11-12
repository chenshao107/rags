package vectordb

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseVectorConfig(t *testing.T) {
	unnamed := json.RawMessage(`{"size":4096,"distance":"Cosine","on_disk":false}`)
	size, distance, err := parseVectorConfig(unnamed)
	if err != nil {
		t.Fatal(err)
	}
	if size != 4096 || distance != "Cosine" {
		t.Fatalf("unnamed: got %d %s", size, distance)
	}

	named := json.RawMessage(`{"text":{"size":1024,"distance":"Dot"}}`)
	size, distance, err = parseVectorConfig(named)
	if err != nil {
		t.Fatal(err)
	}
	if size != 1024 || distance != "Dot" {
		t.Fatalf("named: got %d %s", size, distance)
	}

	if _, _, err := parseVectorConfig(json.RawMessage(`{"a":{"size":1},"b":{"size":2}}`)); err == nil {
		t.Fatal("expected error for multiple named vectors")
	}
}

func TestUploadSnapshotStreamsFile(t *testing.T) {
	const payload = "snapshot-bytes"
	var gotChecksum, gotPriority string
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/demo/snapshots/upload" {
			http.NotFound(w, r)
			return
		}
		gotChecksum = r.URL.Query().Get("checksum")
		gotPriority = r.URL.Query().Get("priority")
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Errorf("part: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		buf, err := io.ReadAll(part)
		if err != nil {
			t.Errorf("read part: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":true,"status":"ok"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "snap")
	if err := os.WriteFile(path, []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}

	client := NewClient(srv.URL)
	if err := client.UploadSnapshot("demo", path, "abc123"); err != nil {
		t.Fatal(err)
	}
	if gotBody != payload {
		t.Fatalf("uploaded %q", gotBody)
	}
	if gotChecksum != "abc123" || gotPriority != "snapshot" {
		t.Fatalf("query checksum=%s priority=%s", gotChecksum, gotPriority)
	}
}
