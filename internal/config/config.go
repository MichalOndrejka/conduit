// Package config is the Go port of app/config.py: JSON config file with
// env-var overrides for Docker/cloud deploys.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type EmbeddingConfig struct {
	Provider       string `json:"provider"` // "openai-compatible" (Ollama, etc.) | "azure-openai"
	Model          string `json:"model"`
	BaseURL        string `json:"base_url"`
	Dimensions     int    `json:"dimensions"`
	MaxInputTokens int    `json:"max_input_tokens"` // embedding model's context window in tokens
	// Azure OpenAI — only used when Provider == "azure-openai"
	AzureEndpoint         string `json:"azure_endpoint"`
	AzureDeployment       string `json:"azure_deployment"`
	AzureAPIVersion       string `json:"azure_api_version"`
	AzureAPIKeyCredential string `json:"azure_api_key_credential"` // credential name in the secrets store
}

type QdrantConfig struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	HTTPS  bool   `json:"https"`
	APIKey string `json:"api_key"`
}

type ChunkingConfig struct {
	MaxChunkSize int `json:"max_chunk_size"`
	Overlap      int `json:"overlap"`
}

type PreprocessingConfig struct {
	Provider     string          `json:"provider"` // "openai-compatible" (Ollama, etc.) | "azure-openai"
	BaseURL      string          `json:"base_url"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	SourceTypes  map[string]bool `json:"source_types"`
	// Azure OpenAI — only used when Provider == "azure-openai"
	AzureEndpoint         string `json:"azure_endpoint"`
	AzureDeployment       string `json:"azure_deployment"`
	AzureAPIVersion       string `json:"azure_api_version"`
	AzureAPIKeyCredential string `json:"azure_api_key_credential"` // credential name in the secrets store
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
			Provider:        "openai-compatible",
			Model:           "nomic-embed-text-v2-moe",
			BaseURL:         "http://localhost:11434/v1",
			Dimensions:      768,
			MaxInputTokens:  8192,
			AzureAPIVersion: "2024-02-01",
		},
		Qdrant:   QdrantConfig{Host: "localhost", Port: 6333},
		Chunking: ChunkingConfig{MaxChunkSize: 2000, Overlap: 200},
		Preprocessing: PreprocessingConfig{
			Provider:        "openai-compatible",
			AzureAPIVersion: "2024-02-01",
			SourceTypes: map[string]bool{
				"workitem": true, "requirements": true, "test-case": true,
				"test-results": true, "git-commits": false, "code": false,
				"testcode": false, "pipeline-build": true, "documentation": true,
			},
		},
		SourcesFilePath: "conduit-sources.json",
	}
}

func configPath() string {
	if p := os.Getenv("CONDUIT_CONFIG"); p != "" {
		return p
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

	if v := os.Getenv("QDRANT_HOST"); v != "" {
		cfg.Qdrant.Host = v
	}
	if v := os.Getenv("QDRANT_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Qdrant.Port = n
		}
	}
	if v := os.Getenv("QDRANT_HTTPS"); v != "" {
		s := strings.ToLower(strings.TrimSpace(v))
		cfg.Qdrant.HTTPS = s == "1" || s == "true" || s == "yes"
	}
	if v := os.Getenv("QDRANT_API_KEY"); v != "" {
		cfg.Qdrant.APIKey = v
	}
	if v := os.Getenv("EMBEDDING_PROVIDER"); v != "" {
		cfg.Embedding.Provider = v
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
	if v := os.Getenv("PREPROCESSING_PROVIDER"); v != "" {
		cfg.Preprocessing.Provider = v
	}
	if v := os.Getenv("PREPROCESSING_MODEL"); v != "" {
		cfg.Preprocessing.Model = v
	}
	if v := os.Getenv("PREPROCESSING_BASE_URL"); v != "" {
		cfg.Preprocessing.BaseURL = v
	}
	if v := os.Getenv("AZURE_OPENAI_ENDPOINT"); v != "" {
		cfg.Embedding.AzureEndpoint = v
	}
	if v := os.Getenv("AZURE_OPENAI_DEPLOYMENT"); v != "" {
		cfg.Embedding.AzureDeployment = v
	}
	if v := os.Getenv("AZURE_OPENAI_API_VERSION"); v != "" {
		cfg.Embedding.AzureAPIVersion = v
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
