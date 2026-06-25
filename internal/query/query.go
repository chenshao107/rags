package query

import (
	"fmt"
	"sort"
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

// MergeResults merges multiple result sets, keeping the best score per chunk (by file+line)
func MergeResults(sets ...[]Result) []Result {
	best := make(map[string]Result) // key: "file:startLine:endLine"
	for _, results := range sets {
		for _, r := range results {
			key := fmt.Sprintf("%s:%d:%d", r.FilePath, r.StartLine, r.EndLine)
			if existing, ok := best[key]; !ok || r.Score > existing.Score {
				best[key] = r
			}
		}
	}

	// Sort by score descending
	type indexed struct {
		r     Result
		index int
	}
	var sorted []indexed
	for _, r := range best {
		sorted = append(sorted, indexed{r: r})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].r.Score > sorted[j].r.Score })
	for i := range sorted {
		sorted[i].index = i
	}

	out := make([]Result, len(sorted))
	for i, s := range sorted {
		out[i] = s.r
	}
	return out
}

// Dedup keeps only the highest-scoring result per file, preserving original order
func Dedup(results []Result) []Result {
	type best struct {
		index int
		r     Result
	}
	seen := make(map[string]*best, len(results))
	for i, r := range results {
		if existing, ok := seen[r.FilePath]; ok {
			if r.Score > existing.r.Score {
				existing.r = r
			}
		} else {
			seen[r.FilePath] = &best{index: i, r: r}
		}
	}

	// Build result preserving original order
	type item struct {
		index int
		r     Result
	}
	var items []item
	for _, v := range seen {
		items = append(items, item{index: v.index, r: v.r})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].index < items[j].index })

	out := make([]Result, 0, len(items))
	for _, it := range items {
		out = append(out, it.r)
	}
	return out
}

// FormatResults formats search results for display
func FormatResults(results []Result) string {
	var sb strings.Builder
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("[%.2f] %s:%d-%d\n", r.Score, r.FilePath, r.StartLine, r.EndLine))
		text := safeTruncate(r.Text, 200)
		sb.WriteString(text)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

// FormatExplore formats results as an agent-friendly reading path
func FormatExplore(results []Result, query string) string {
	var sb strings.Builder

	sb.WriteString("══ rags explore ══\n")
	sb.WriteString(fmt.Sprintf("Query: %s\n\n", query))

	deduped := Dedup(results)

	if len(deduped) == 0 {
		sb.WriteString("No results found.\n")
		return sb.String()
	}

	// Category bands: each result goes into exactly one band
	type catDef struct {
		label string
		min   float64
		max   float64
		desc  string
	}
	categories := []catDef{
		{"🔴 必读", 0.80, 1.01, "高度相关 — 优先阅读"},
		{"🟡 推荐", 0.50, 0.80, "中等相关 — 值得浏览"},
		{"🟢 参考", 0.00, 0.50, "可能相关 — 快速扫一眼"},
	}

	for _, cat := range categories {
		var matched []Result
		for _, r := range deduped {
			if r.Score >= cat.min && r.Score < cat.max {
				matched = append(matched, r)
			}
		}
		if len(matched) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("── %s (%s) ──\n", cat.label, cat.desc))
		for i, r := range matched {
			label := fileTypeLabel(r.FilePath)
			sb.WriteString(fmt.Sprintf("%d. [%.2f] %s %s:%d-%d\n",
				i+1, r.Score, label, r.FilePath, r.StartLine, r.EndLine))
			text := safeTruncate(r.Text, 120)
			sb.WriteString(fmt.Sprintf("   \"%s\"\n", text))
		}
		sb.WriteString("\n")
	}

	// Suggested reading order
	sb.WriteString("── 📖 建议阅读路径 ──\n")
	sb.WriteString("优先阅读 🔴 标记的文件，然后按需浏览 🟡 。\n")
	sb.WriteString("如果搜索结果指向「大纲」「细化大纲」「人物卡」，\n")
	sb.WriteString("建议先看这些参考文件建立全局认知，再跳读正文章节。\n")
	sb.WriteString("正文章节通常需要按时间顺序阅读（从小到大排序章号）。\n")

	return sb.String()
}

func fileTypeLabel(path string) string {
	switch {
	case strings.Contains(path, "大纲"), strings.Contains(path, "时间线"):
		return "[参考]"
	case strings.Contains(path, "人物卡"):
		return "[设定]"
	case strings.Contains(path, "正文"):
		return "[正文]"
	default:
		return "[其他]"
	}
}

// safeTruncate truncates text to n runes without breaking UTF-8
func safeTruncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
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
