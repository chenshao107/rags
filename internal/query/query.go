package query

import (
	"fmt"
	"strings"

	"github.com/chenshao/rags/internal/embedder"
	"github.com/chenshao/rags/internal/reranker"
	"github.com/chenshao/rags/internal/vectordb"
)

// Result is a single search result with all metadata
type Result struct {
	Score     float64
	FilePath  string
	StartLine int
	EndLine   int
	Text      string
}

// Searcher runs the search pipeline
type Searcher struct {
	VDB      *vectordb.Client
	Embedder *embedder.Client
	Reranker *reranker.Client
}

// New creates a Searcher
func New(vdb *vectordb.Client, emb *embedder.Client, rerank *reranker.Client) *Searcher {
	return &Searcher{VDB: vdb, Embedder: emb, Reranker: rerank}
}

// Search runs the full pipeline: embed → search → optional rerank → top results
func (s *Searcher) Search(collection, projectName, query string, topK int) ([]Result, error) {
	// Step 1: Embed query
	vec, err := s.Embedder.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Step 2: Vector search (top 50 for reranking, else topK)
	searchLimit := 50
	if s.Reranker == nil {
		searchLimit = topK
	}

	filter := vectordb.ProjectFilter(projectName)
	rawResults, err := s.VDB.Search(collection, vec, searchLimit, filter)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	if len(rawResults) == 0 {
		return nil, nil
	}

	// Step 3: Rerank if available
	if s.Reranker != nil && len(rawResults) > topK {
		docs := make([]string, len(rawResults))
		for i, r := range rawResults {
			docs[i] = payloadString(r.Payload, "text")
		}

		rerankResults, err := s.Reranker.Rerank(query, docs, topK)
		if err != nil {
			// Rerank failed — fall back to vector-only topK
			rawResults = rawResults[:topK]
		} else {
			// Reorder by rerank score
			reordered := make([]vectordb.SearchResult, 0, len(rerankResults))
			for _, rr := range rerankResults {
				if rr.Index < len(rawResults) {
					r := rawResults[rr.Index]
					r.Score = rr.RelevanceScore
					reordered = append(reordered, r)
				}
			}
			rawResults = reordered
		}
	} else if len(rawResults) > topK {
		rawResults = rawResults[:topK]
	}

	// Step 4: Convert to structured results
	results := make([]Result, 0, len(rawResults))
	for _, r := range rawResults {
		results = append(results, Result{
			Score:     r.Score,
			FilePath:  payloadString(r.Payload, "file_path"),
			StartLine: payloadInt(r.Payload, "start_line"),
			EndLine:   payloadInt(r.Payload, "end_line"),
			Text:      payloadString(r.Payload, "text"),
		})
	}

	return results, nil
}

// FormatResults formats search results for display
func FormatResults(results []Result) string {
	var sb strings.Builder
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("[%.2f] %s:%d-%d\n", r.Score, r.FilePath, r.StartLine, r.EndLine))
		// Truncate text for display
		text := r.Text
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		sb.WriteString(text)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

func payloadString(payload map[string]any, key string) string {
	v, ok := payload[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func payloadInt(payload map[string]any, key string) int {
	v, ok := payload[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
