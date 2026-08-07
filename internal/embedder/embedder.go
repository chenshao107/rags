package embedder

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxEmbedAttempts = 8

// Client calls a SiliconFlow-compatible embedding API
type Client struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dimensions int
	HTTP       *http.Client
}

type embedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
	Dimensions     int      `json:"dimensions,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

type embedHTTPError struct {
	StatusCode int
	Body       string
}

func (e *embedHTTPError) Error() string {
	return fmt.Sprintf("embed failed (%d): %s", e.StatusCode, e.Body)
}

// NewClient creates an embedding client
func NewClient(baseURL, apiKey, model string, dimensions int) *Client {
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		Dimensions: dimensions,
		HTTP: &http.Client{
			Timeout: 5 * time.Minute,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 32,
				// Avoid long-lived HTTP/2 connections that trigger GOAWAY under load.
				TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
			},
		},
	}
}

// Embed returns the embedding vector for a single text
func (c *Client) Embed(text string) ([]float32, error) {
	results, err := c.EmbedBatch([]string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return results[0], nil
}

// EmbedBatch returns embedding vectors for multiple texts
func (c *Client) EmbedBatch(texts []string) ([][]float32, error) {
	req := embedRequest{
		Model:          c.Model,
		Input:          texts,
		EncodingFormat: "float",
	}
	if c.Dimensions > 0 {
		req.Dimensions = c.Dimensions
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	url := c.BaseURL + "/embeddings"
	var lastErr error
	for attempt := 0; attempt < maxEmbedAttempts; attempt++ {
		if attempt > 0 {
			wait := embedRetryBackoff(attempt - 1)
			fmt.Fprintf(os.Stderr, "  embed retry %d/%d in %v: %v\n", attempt, maxEmbedAttempts-1, wait, lastErr)
			time.Sleep(wait)
		}

		vectors, err := c.embedBatchOnce(body, url)
		if err == nil {
			return vectors, nil
		}
		lastErr = err
		if !isRetryableEmbedError(err) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("embed failed after %d attempts: %w", maxEmbedAttempts, lastErr)
}

func (c *Client) embedBatchOnce(body []byte, url string) ([][]float32, error) {
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embed response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &embedHTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var result embedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}

	vectors := make([][]float32, len(result.Data))
	for _, d := range result.Data {
		vectors[d.Index] = d.Embedding
	}
	return vectors, nil
}

func embedRetryBackoff(attempt int) time.Duration {
	wait := time.Duration(attempt+1) * 3 * time.Second
	if wait > 30*time.Second {
		return 30 * time.Second
	}
	return wait
}

func isRetryableEmbedError(err error) bool {
	if err == nil {
		return false
	}

	var httpErr *embedHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "goaway") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "decode embed response") ||
		strings.Contains(msg, "read embed response") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "broken pipe")
}
