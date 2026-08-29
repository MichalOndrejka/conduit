// Package config is the Go port of app/config.py: JSON config file with
// env-var overrides for Docker/cloud deploys.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

type EmbeddingConfig struct {
	Model          string `json:"model"`
	BaseURL        string `json:"base_url"`
	Dimensions     int    `json:"dimensions"`
	MaxInputTokens int    `json:"max_input_tokens"` // embedding model's context window in tokens
	Concurrency    int    `json:"concurrency"`      // max in-flight embed calls during a sync
}

type QdrantConfig struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

type ChunkingConfig struct {
	MaxChunkSize int `json:"max_chunk_size"`
	Overlap      int `json:"overlap"`
}

type PreprocessingConfig struct {
	Enabled      bool            `json:"enabled"`
	BaseURL      string          `json:"base_url"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	SourceTypes  map[string]bool `json:"source_types"`
	Concurrency  int             `json:"concurrency"` // max in-flight summarize calls during a sync
}

type AppConfig struct {
	Embedding       EmbeddingConfig     `json:"embedding"`
	Qdrant          QdrantConfig        `json:"qdrant"`
	Chunking        ChunkingConfig      `json:"chunking"`
	Preprocessing   PreprocessingConfig `json:"preprocessing"`
	SourcesFilePath string              `json:"sources_file_path"`
}

func defaults() AppConfig {
	return AppConfig{
		Embedding: EmbeddingConfig{
			Model:          "nomic-embed-text-v2-moe",
			BaseURL:        "http://localhost:11434/v1",
			Dimensions:     768,
			MaxInputTokens: 8192,
			Concurrency:    4,
		},
		Qdrant:   QdrantConfig{URL: "http://localhost:6333"},
		Chunking: ChunkingConfig{MaxChunkSize: 2000, Overlap: 200},
		Preprocessing: PreprocessingConfig{
			Concurrency: 4,
			SourceTypes: map[string]bool{
				"work-item": true, "requirements": true, "test-case": true,
				"commit-history": false, "code": false,
				"test-code": false, "documentation": true,
			},
		},
		SourcesFilePath: "conduit-sources.json",
	}
}

func configPath() string {
	if p := os.Getenv("CONDUIT_CONFIG"); p != "" {
		return p
	}
	if d := os.Getenv("CONDUIT_DATA_DIR"); d != "" {
		return filepath.Join(d, "config.json")
	}
	return "config.json"
}

// Load reads config.json (if present) and applies env-var overrides,
// mirroring app/config.py:load_config.
func Load() (*AppConfig, error) {
	cfg := defaults()
	if data, err := os.ReadFile(configPath()); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
	}

	if v := os.Getenv("QDRANT_URL"); v != "" {
		cfg.Qdrant.URL = v
	}
	if v := os.Getenv("QDRANT_API_KEY"); v != "" {
		cfg.Qdrant.APIKey = v
	}
	if v := os.Getenv("EMBEDDING_MODEL"); v != "" {
		cfg.Embedding.Model = v
	}
	if v := os.Getenv("EMBEDDING_BASE_URL"); v != "" {
		cfg.Embedding.BaseURL = v
	}
	if v := os.Getenv("EMBEDDING_DIMENSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Embedding.Dimensions = n
		}
	}
	if v := os.Getenv("EMBEDDING_MAX_INPUT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Embedding.MaxInputTokens = n
		}
	}
	if v := os.Getenv("EMBEDDING_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Embedding.Concurrency = n
		}
	}
	if v := os.Getenv("PREPROCESSING_MODEL"); v != "" {
		cfg.Preprocessing.Model = v
	}
	if v := os.Getenv("PREPROCESSING_BASE_URL"); v != "" {
		cfg.Preprocessing.BaseURL = v
	}
	if v := os.Getenv("PREPROCESSING_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Preprocessing.Concurrency = n
		}
	}
	if v := os.Getenv("CONDUIT_DATA_DIR"); v != "" {
		if err := os.MkdirAll(v, 0o755); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(cfg.SourcesFilePath) {
			cfg.SourcesFilePath = filepath.Join(v, filepath.Base(cfg.SourcesFilePath))
		}
	}
	return &cfg, nil
}

// DataDir returns the directory holding sources/credentials/config state.
func DataDir(cfg *AppConfig) string {
	return filepath.Dir(cfg.SourcesFilePath)
}

// Path returns the resolved config file path, for display on the Settings page.
func Path() string {
	return configPath()
}

// Save persists cfg to the config file as JSON, mirroring save_config in
// app/config.py. Connection-affecting fields (embedding/Qdrant) take effect on
// the next restart, since the RAG services capture their config at startup.
func Save(cfg *AppConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := configPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// path may be a bind-mounted single file (its own mount point), which
		// forbids renaming over it (EBUSY) — fall back to an in-place write.
		_ = os.Remove(tmp)
		return os.WriteFile(path, data, 0o644)
	}
	return nil
}
