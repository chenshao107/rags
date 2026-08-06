package app

import (
	"fmt"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/embedder"
	"github.com/chenshao/rags/internal/query"
	"github.com/chenshao/rags/internal/reranker"
	"github.com/chenshao/rags/internal/vectordb"
)

// SearchOptions controls semantic search behavior.
type SearchOptions struct {
	ProjectRoot string
	Queries     []string
	TopK        int
	Dedup       bool
	Explore     bool
	PathPrefix  string
}

// SearchResult holds formatted output from a search run.
type SearchResult struct {
	Output       string
	DisplayQuery string
	ResultCount  int
}

// RunSearch executes the semantic search pipeline for a project.
func RunSearch(pcfg config.ProjectConfig, settings config.GlobalSettings, opts SearchOptions) (SearchResult, error) {
	if len(opts.Queries) == 0 {
		return SearchResult{}, fmt.Errorf("no query provided")
	}
	if opts.TopK <= 0 {
		opts.TopK = 5
	}

	if opts.ProjectRoot == "" {
		return SearchResult{}, fmt.Errorf("project root is required")
	}

	collectionName, err := config.ResolveCollection(opts.ProjectRoot, pcfg)
	if err != nil {
		return SearchResult{}, fmt.Errorf("resolve collection: %w", err)
	}

	vdb := vectordb.NewClient(settings.Qdrant.URL)
	emb := embedder.NewClient(settings.Embedding.BaseURL, settings.Embedding.APIKey, settings.Embedding.Model)

	var rr *reranker.Client
	if settings.Rerank != nil && settings.Rerank.APIKey != "" {
		rr = reranker.NewClient(settings.Rerank.BaseURL, settings.Rerank.APIKey, settings.Rerank.Model)
	}

	searcher := query.New(vdb, emb, rr)
	fetchK := opts.TopK
	if opts.Dedup || opts.Explore {
		fetchK = opts.TopK * 3
		if fetchK > 50 {
			fetchK = 50
		}
	}

	var allResults []query.Result
	displayQuery := opts.Queries[0]
	for i, q := range opts.Queries {
		results, err := searcher.Search(collectionName, pcfg.Name, q, fetchK, opts.PathPrefix)
		if err != nil {
			return SearchResult{}, fmt.Errorf("search %q: %w", q, err)
		}
		if i == 0 && len(opts.Queries) == 1 {
			allResults = results
		} else {
			allResults = query.MergeResults(allResults, results)
		}
	}

	if len(allResults) == 0 {
		return SearchResult{Output: "No results found.\n", DisplayQuery: displayQuery}, nil
	}

	if opts.Dedup {
		allResults = query.Dedup(allResults)
	}
	if len(allResults) > opts.TopK {
		allResults = allResults[:opts.TopK]
	}

	var output string
	if opts.Explore {
		output = query.FormatExplore(allResults, displayQuery)
	} else {
		output = query.FormatResults(allResults)
		if output != "" {
			output += "\n"
		}
	}

	return SearchResult{
		Output:       output,
		DisplayQuery: displayQuery,
		ResultCount:  len(allResults),
	}, nil
}
