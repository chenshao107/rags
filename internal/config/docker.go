package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// WriteSettingsFromEnv writes settings.yaml using standard Docker environment variables.
func WriteSettingsFromEnv(dir string) (GlobalSettings, error) {
	settings := DefaultSettings()

	if v := os.Getenv("QDRANT_URL"); v != "" {
		settings.Qdrant.URL = v
	}
	if v := os.Getenv("EMBEDDING_BASE_URL"); v != "" {
		settings.Embedding.BaseURL = v
	}
	if v := os.Getenv("EMBEDDING_MODEL"); v != "" {
		settings.Embedding.Model = v
	}
	if v := os.Getenv("SILICONFLOW_API_KEY"); v != "" {
		settings.Embedding.APIKey = v
	}

	if settings.Embedding.APIKey == "" {
		return settings, fmt.Errorf("SILICONFLOW_API_KEY is required")
	}

	rerankModel := os.Getenv("RERANK_MODEL")
	if rerankModel == "" {
		rerankModel = "BAAI/bge-reranker-v2-m3"
	}
	settings.Rerank = &RerankConfig{
		Provider: "siliconflow",
		BaseURL:  settings.Embedding.BaseURL,
		APIKey:   settings.Embedding.APIKey,
		Model:    rerankModel,
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return settings, fmt.Errorf("create settings dir: %w", err)
	}
	data, err := yaml.Marshal(&settings)
	if err != nil {
		return settings, fmt.Errorf("marshal settings: %w", err)
	}
	path := filepath.Join(dir, SettingsFile)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return settings, fmt.Errorf("write settings: %w", err)
	}
	return settings, nil
}
