package vectordb

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// CollectionMeta is the part of a Qdrant collection needed to move it.
type CollectionMeta struct {
	PointsCount uint64
	VectorSize  int
	Distance    string
}

// SnapshotDesc is a collection snapshot created on the Qdrant node.
type SnapshotDesc struct {
	Name string
	Size int64
}

type snapshotCreateResponse struct {
	Result SnapshotDescJSON `json:"result"`
}

// SnapshotDescJSON is the snapshot object returned by Qdrant.
type SnapshotDescJSON struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type collectionMetaResponse struct {
	Result struct {
		PointsCount         uint64 `json:"points_count"`
		IndexedVectorsCount uint64 `json:"indexed_vectors_count"`
		Config              struct {
			Params struct {
				Vectors json.RawMessage `json:"vectors"`
			} `json:"params"`
		} `json:"config"`
	} `json:"result"`
}

// CollectionMeta reads vector size, distance, and point count.
func (c *Client) CollectionMeta(name string) (CollectionMeta, error) {
	endpoint := fmt.Sprintf("%s/collections/%s", c.BaseURL, url.PathEscape(name))
	resp, err := c.HTTP.Get(endpoint)
	if err != nil {
		return CollectionMeta{}, fmt.Errorf("read collection %s: %w", name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return CollectionMeta{}, fmt.Errorf("read collection %s: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return CollectionMeta{}, fmt.Errorf("read collection %s failed (%d): %s", name, resp.StatusCode, trimBody(body))
	}

	var parsed collectionMetaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return CollectionMeta{}, fmt.Errorf("decode collection %s: %w", name, err)
	}
	size, distance, err := parseVectorConfig(parsed.Result.Config.Params.Vectors)
	if err != nil {
		return CollectionMeta{}, fmt.Errorf("collection %s: %w", name, err)
	}
	points := parsed.Result.PointsCount
	if parsed.Result.IndexedVectorsCount > points {
		points = parsed.Result.IndexedVectorsCount
	}
	return CollectionMeta{
		PointsCount: points,
		VectorSize:  size,
		Distance:    distance,
	}, nil
}

func parseVectorConfig(raw json.RawMessage) (size int, distance string, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, "", fmt.Errorf("collection has no vector config")
	}
	var single struct {
		Size     int    `json:"size"`
		Distance string `json:"distance"`
	}
	if err := json.Unmarshal(raw, &single); err != nil {
		return 0, "", fmt.Errorf("parse vector config: %w", err)
	}
	if single.Size > 0 {
		if single.Distance == "" {
			single.Distance = "Cosine"
		}
		return single.Size, single.Distance, nil
	}

	var named map[string]struct {
		Size     int    `json:"size"`
		Distance string `json:"distance"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		return 0, "", fmt.Errorf("parse vector config: %w", err)
	}
	if len(named) != 1 {
		return 0, "", fmt.Errorf("expected one vector, found %d", len(named))
	}
	for _, cfg := range named {
		if cfg.Size <= 0 {
			return 0, "", fmt.Errorf("vector size is missing")
		}
		if cfg.Distance == "" {
			cfg.Distance = "Cosine"
		}
		return cfg.Size, cfg.Distance, nil
	}
	return 0, "", fmt.Errorf("vector size is missing")
}

// CreateSnapshot asks Qdrant to snapshot a collection and waits until it is ready.
func (c *Client) CreateSnapshot(collection string) (SnapshotDesc, error) {
	endpoint := fmt.Sprintf("%s/collections/%s/snapshots?wait=true", c.BaseURL, url.PathEscape(collection))
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return SnapshotDesc{}, fmt.Errorf("create snapshot: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return SnapshotDesc{}, fmt.Errorf("create snapshot: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return SnapshotDesc{}, fmt.Errorf("create snapshot: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return SnapshotDesc{}, fmt.Errorf("create snapshot failed (%d): %s", resp.StatusCode, trimBody(body))
	}

	var parsed snapshotCreateResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return SnapshotDesc{}, fmt.Errorf("decode snapshot: %w", err)
	}
	if parsed.Result.Name == "" || strings.ContainsAny(parsed.Result.Name, `/\`) || parsed.Result.Name == ".." {
		return SnapshotDesc{}, fmt.Errorf("create snapshot returned an invalid name: %s", trimBody(body))
	}
	return SnapshotDesc{Name: parsed.Result.Name, Size: parsed.Result.Size}, nil
}

// DownloadSnapshot streams a collection snapshot into dst.
func (c *Client) DownloadSnapshot(collection, snapshot string, dst io.Writer) (int64, error) {
	endpoint := fmt.Sprintf("%s/collections/%s/snapshots/%s", c.BaseURL, url.PathEscape(collection), url.PathEscape(snapshot))
	resp, err := c.HTTP.Get(endpoint)
	if err != nil {
		return 0, fmt.Errorf("download snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("download snapshot failed (%d): %s", resp.StatusCode, trimBody(body))
	}
	n, err := io.Copy(dst, resp.Body)
	if err != nil {
		return n, fmt.Errorf("download snapshot: %w", err)
	}
	return n, nil
}

// DeleteSnapshot removes a snapshot file kept on the Qdrant node.
func (c *Client) DeleteSnapshot(collection, snapshot string) error {
	endpoint := fmt.Sprintf("%s/collections/%s/snapshots/%s", c.BaseURL, url.PathEscape(collection), url.PathEscape(snapshot))
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("delete snapshot failed (%d): %s", resp.StatusCode, trimBody(body))
	}
	return nil
}

// UploadSnapshot recovers a collection from a snapshot file.
// Qdrant creates the collection when it does not exist, and priority=snapshot
// replaces data when it does. checksum is the hex SHA-256 of the file.
func (c *Client) UploadSnapshot(collection, path, checksum string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat snapshot: %w", err)
	}

	body, contentType, contentLength, err := snapshotMultipart(f, info.Size())
	if err != nil {
		return err
	}

	query := url.Values{}
	query.Set("wait", "true")
	query.Set("priority", "snapshot")
	if checksum != "" {
		query.Set("checksum", checksum)
	}
	endpoint := fmt.Sprintf("%s/collections/%s/snapshots/upload?%s", c.BaseURL, url.PathEscape(collection), query.Encode())
	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("upload snapshot: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = contentLength

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("upload snapshot: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload snapshot failed (%d): %s", resp.StatusCode, trimBody(respBody))
	}
	return nil
}

// snapshotMultipart builds a multipart body with a known length so the snapshot
// file is streamed instead of buffered.
func snapshotMultipart(file io.Reader, size int64) (io.Reader, string, int64, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, "", 0, fmt.Errorf("multipart boundary: %w", err)
	}
	boundary := "rags" + hex.EncodeToString(raw[:])
	prefix := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"snapshot\"; filename=\"snapshot.snapshot\"\r\nContent-Type: application/octet-stream\r\n\r\n", boundary)
	suffix := fmt.Sprintf("\r\n--%s--\r\n", boundary)
	body := io.MultiReader(strings.NewReader(prefix), file, strings.NewReader(suffix))
	return body, "multipart/form-data; boundary=" + boundary, int64(len(prefix)+len(suffix)) + size, nil
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 1024 {
		return s[:1024] + "..."
	}
	return s
}
