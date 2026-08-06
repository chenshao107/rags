package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/chenshao/rags/internal/config"
)

const defaultTimeout = 60 * time.Second

// SearchRequest is sent to a public rags server.
type SearchRequest struct {
	Queries    []string `json:"queries"`
	TopK       int      `json:"top_k"`
	Dedup      bool     `json:"dedup"`
	Explore    bool     `json:"explore"`
	PathPrefix string   `json:"path_prefix,omitempty"`
}

// SearchResponse is returned by a public rags server.
type SearchResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// RemoteSearch sends a search request to a public rags server endpoint.
func RemoteSearch(endpoint string, req SearchRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	searchURL, err := config.ClientSearchURL(endpoint)
	if err != nil {
		return "", err
	}

	httpClient := &http.Client{Timeout: defaultTimeout}
	if config.IsUnixEndpoint(endpoint) {
		socketPath, err := config.UnixSocketPath(endpoint)
		if err != nil {
			return "", err
		}
		httpClient.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 10 * time.Second}
				return d.DialContext(ctx, "unix", socketPath)
			},
		}
	}

	httpReq, err := http.NewRequest(http.MethodPost, searchURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("connect to public rags server (%s): %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("public rags server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out SearchResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Output, nil
}
