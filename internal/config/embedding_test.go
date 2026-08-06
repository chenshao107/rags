package config

import "testing"

func TestEmbeddingVectorSize(t *testing.T) {
	if got := EmbeddingVectorSize(EmbeddingConfig{Model: DefaultEmbeddingModel, Dimensions: 4096}); got != 4096 {
		t.Fatalf("explicit dimensions: got %d", got)
	}
	if got := EmbeddingVectorSize(EmbeddingConfig{Model: "Qwen/Qwen3-Embedding-8B"}); got != 4096 {
		t.Fatalf("qwen3-8b infer: got %d", got)
	}
	if got := EmbeddingVectorSize(EmbeddingConfig{Model: "BAAI/bge-m3"}); got != 1024 {
		t.Fatalf("bge-m3 infer: got %d", got)
	}
}
