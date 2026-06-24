package vectordb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client wraps Qdrant REST API
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Point represents a point to upsert in Qdrant
type Point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// SearchResult is a single result from a search query
type SearchResult struct {
	ID      string         `json:"id"`
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

// searchRequest is the body for POST /collections/{name}/points/search
type searchRequest struct {
	Vector      []float32            `json:"vector"`
	Limit       int                  `json:"limit"`
	WithPayload bool                 `json:"with_payload"`
	Filter      *filter              `json:"filter,omitempty"`
}

// searchResponse is the response from a search query
type searchResponse struct {
	Result []SearchResult `json:"result"`
}

// upsertRequest is the body for PUT /collections/{name}/points
type upsertRequest struct {
	Points []Point `json:"points"`
}

// deleteRequest is the body for POST /collections/{name}/points/delete
type deleteRequest struct {
	Filter filter `json:"filter"`
}

// filter is a simplified Qdrant filter
type filter struct {
	Must []condition `json:"must"`
}

type condition struct {
	Key   string     `json:"key"`
	Match matchValue `json:"match"`
}

type matchValue struct {
	Value string `json:"value"`
}

// createCollectionRequest is the body for PUT /collections/{name}
type createCollectionRequest struct {
	Vectors vectorsConfig `json:"vectors"`
}

type vectorsConfig struct {
	Size     int    `json:"size"`
	Distance string `json:"distance"`
}

// collectionResponse is the response from GET /collections/{name}
type collectionResponse struct {
	Result collectionInfo `json:"result"`
}

type collectionInfo struct {
	Exists bool `json:"exists"`
}

// NewClient creates a Qdrant client
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{},
	}
}

// CollectionExists checks if a collection exists
func (c *Client) CollectionExists(name string) (bool, error) {
	url := fmt.Sprintf("%s/collections/%s", c.BaseURL, name)
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return false, fmt.Errorf("check collection: %w", err)
	}
	defer resp.Body.Close()

	// 200 = exists, 404 = not found
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// EnsureCollection creates a collection if it doesn't exist
// For bge-m3, the vector dimension is 1024
func (c *Client) EnsureCollection(name string, vectorSize int) error {
	exists, err := c.CollectionExists(name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	req := createCollectionRequest{
		Vectors: vectorsConfig{
			Size:     vectorSize,
			Distance: "Cosine",
		},
	}
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/collections/%s", c.BaseURL, name)
	httpReq, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("create collection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create collection failed (%d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Upsert inserts or updates points
func (c *Client) Upsert(collection string, points []Point) error {
	req := upsertRequest{Points: points}
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/collections/%s/points?wait=true", c.BaseURL, collection)
	httpReq, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert failed (%d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Search performs a vector similarity search
func (c *Client) Search(collection string, vector []float32, limit int, filter *filter) ([]SearchResult, error) {
	req := searchRequest{
		Vector:      vector,
		Limit:       limit,
		WithPayload: true,
		Filter:      filter,
	}
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/collections/%s/points/search", c.BaseURL, collection)
	httpReq, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	return result.Result, nil
}

// DeleteByFilePath deletes all points matching a file_path
func (c *Client) DeleteByFilePath(collection, filePath string) error {
	req := deleteRequest{
		Filter: filter{
			Must: []condition{
				{Key: "file_path", Match: matchValue{Value: filePath}},
			},
		},
	}
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/collections/%s/points/delete?wait=true", c.BaseURL, collection)
	httpReq, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("delete by file_path: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed (%d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// DeleteCollection removes an entire collection
func (c *Client) DeleteCollection(name string) error {
	url := fmt.Sprintf("%s/collections/%s", c.BaseURL, name)
	httpReq, _ := http.NewRequest(http.MethodDelete, url, nil)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}
	defer resp.Body.Close()

	// 404 means already gone, that's fine
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete collection failed (%d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ProjectFilter creates a filter for a specific project
func ProjectFilter(projectName string) *filter {
	return &filter{
		Must: []condition{
			{Key: "project", Match: matchValue{Value: projectName}},
		},
	}
}
