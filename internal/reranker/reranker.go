package reranker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client calls a SiliconFlow-compatible rerank API
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

type rerankRequest struct {
	Model       string   `json:"model"`
	Query       string   `json:"query"`
	Documents   []string `json:"documents"`
	TopN        int      `json:"top_n,omitempty"`
	Instruction string   `json:"instruction,omitempty"`
}

type rerankResponse struct {
	Results []rerankResult `json:"results"`
}

type rerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// NewClient creates a rerank client (can be nil if rerank is disabled)
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{},
	}
}

// Rerank re-ranks documents against a query, returns sorted indices with scores
func (c *Client) Rerank(query string, documents []string, topN int) ([]rerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	req := rerankRequest{
		Model:     c.Model,
		Query:     query,
		Documents: documents,
		TopN:      topN,
	}
	if strings.Contains(strings.ToLower(c.Model), "qwen3-reranker") {
		req.Instruction = "Given a search query, retrieve relevant passages that answer the query."
	}
	body, _ := json.Marshal(req)

	url := c.BaseURL + "/rerank"
	httpReq, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rerank failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode rerank response: %w", err)
	}
	return result.Results, nil
}
