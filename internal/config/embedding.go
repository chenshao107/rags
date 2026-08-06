package config

import "strings"

// DefaultEmbeddingModel is the recommended SiliconFlow embedding model.
const DefaultEmbeddingModel = "Qwen/Qwen3-Embedding-8B"

// EmbeddingVectorSize returns the Qdrant vector dimension for an embedding config.
func EmbeddingVectorSize(emb EmbeddingConfig) int {
	if emb.Dimensions > 0 {
		return emb.Dimensions
	}
	return inferEmbeddingVectorSize(emb.Model)
}

func inferEmbeddingVectorSize(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "qwen3-embedding-8b"), strings.Contains(m, "qwen3-vl-embedding-8b"):
		return 4096
	case strings.Contains(m, "qwen3-embedding-4b"):
		return 2560
	case strings.Contains(m, "qwen3-embedding-0.6b"):
		return 1024
	default:
		return 1024
	}
}
