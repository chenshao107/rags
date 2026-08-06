package embedder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client calls a SiliconFlow-compatible embedding API
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

type embedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// NewClient creates an embedding client
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
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
	body, _ := json.Marshal(req)

	url := c.BaseURL + "/embeddings"
	bodyReader := bytes.NewReader(body)

	var resp *http.Response
	for attempt := 0; attempt < 6; attempt++ {
		httpReq, err := http.NewRequest(http.MethodPost, url, bodyReader)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

		resp, err = c.HTTP.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("embed request: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			wait := time.Duration(attempt+1) * 3 * time.Second
			time.Sleep(wait)
			bodyReader = bytes.NewReader(body)
			continue
		}
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}

	vectors := make([][]float32, len(result.Data))
	for _, d := range result.Data {
		vectors[d.Index] = d.Embedding
	}
	return vectors, nil
}
