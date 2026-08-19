package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// useConfigFile points CONDUIT_CONFIG at a fresh (nonexistent by default)
// file inside a t.TempDir(), isolating each test from the real config.json
// in the working directory and from any other test's env var state.
func useConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CONDUIT_CONFIG", path)
	return path
}

func TestLoadDefaultsWhenNoConfigFile(t *testing.T) {
	useConfigFile(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Model != "nomic-embed-text-v2-moe" {
		t.Errorf("Embedding.Model = %q, want default", cfg.Embedding.Model)
	}
	if cfg.Embedding.Concurrency != 4 {
		t.Errorf("Embedding.Concurrency = %d, want 4", cfg.Embedding.Concurrency)
	}
	if cfg.Preprocessing.Concurrency != 4 {
		t.Errorf("Preprocessing.Concurrency = %d, want 4", cfg.Preprocessing.Concurrency)
	}
	if cfg.Qdrant.Host != "localhost" || cfg.Qdrant.Port != 6333 {
		t.Errorf("Qdrant = %+v, want default localhost:6333", cfg.Qdrant)
	}
	if cfg.Chunking.MaxChunkSize != 2000 || cfg.Chunking.Overlap != 200 {
		t.Errorf("Chunking = %+v, want default 2000/200", cfg.Chunking)
	}
	if cfg.SourcesFilePath != "conduit-sources.json" {
		t.Errorf("SourcesFilePath = %q, want default", cfg.SourcesFilePath)
	}
	if !cfg.Preprocessing.SourceTypes["work-item"] || cfg.Preprocessing.SourceTypes["code"] {
		t.Errorf("Preprocessing.SourceTypes = %+v, want default preset", cfg.Preprocessing.SourceTypes)
	}
}

func TestLoadPartialConfigFileKeepsRemainingDefaults(t *testing.T) {
	path := useConfigFile(t)
	if err := os.WriteFile(path, []byte(`{"embedding":{"model":"custom-model"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Model != "custom-model" {
		t.Errorf("Embedding.Model = %q, want %q from config file", cfg.Embedding.Model, "custom-model")
	}
	// Fields absent from the JSON must keep the struct's pre-unmarshal
	// (default) value rather than being zeroed out.
	if cfg.Embedding.Concurrency != 4 {
		t.Errorf("Embedding.Concurrency = %d, want default 4 preserved", cfg.Embedding.Concurrency)
	}
	if cfg.Qdrant.Port != 6333 {
		t.Errorf("Qdrant.Port = %d, want default 6333 preserved", cfg.Qdrant.Port)
	}
}

func TestLoadInvalidJSONErrors(t *testing.T) {
	path := useConfigFile(t)
	if err := os.WriteFile(path, []byte(`{not valid json`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("expected error for malformed config.json, got nil")
	}
}

func TestLoadEnvVarOverrides(t *testing.T) {
	useConfigFile(t)

	t.Setenv("QDRANT_HOST", "qdrant.internal")
	t.Setenv("QDRANT_PORT", "7000")
	t.Setenv("QDRANT_HTTPS", "true")
	t.Setenv("QDRANT_API_KEY", "qd-key")
	t.Setenv("EMBEDDING_PROVIDER", "azure-openai")
	t.Setenv("EMBEDDING_MODEL", "env-model")
	t.Setenv("EMBEDDING_BASE_URL", "http://env-embed/v1")
	t.Setenv("EMBEDDING_DIMENSIONS", "1536")
	t.Setenv("EMBEDDING_MAX_INPUT_TOKENS", "4096")
	t.Setenv("EMBEDDING_CONCURRENCY", "8")
	t.Setenv("PREPROCESSING_PROVIDER", "azure-openai")
	t.Setenv("PREPROCESSING_MODEL", "env-chat-model")
	t.Setenv("PREPROCESSING_BASE_URL", "http://env-chat/v1")
	t.Setenv("PREPROCESSING_CONCURRENCY", "6")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://env.openai.azure.com")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT", "env-deployment")
	t.Setenv("AZURE_OPENAI_API_VERSION", "2025-01-01")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Qdrant.Host", cfg.Qdrant.Host, "qdrant.internal"},
		{"Qdrant.Port", cfg.Qdrant.Port, 7000},
		{"Qdrant.HTTPS", cfg.Qdrant.HTTPS, true},
		{"Qdrant.APIKey", cfg.Qdrant.APIKey, "qd-key"},
		{"Embedding.Provider", cfg.Embedding.Provider, "azure-openai"},
		{"Embedding.Model", cfg.Embedding.Model, "env-model"},
		{"Embedding.BaseURL", cfg.Embedding.BaseURL, "http://env-embed/v1"},
		{"Embedding.Dimensions", cfg.Embedding.Dimensions, 1536},
		{"Embedding.MaxInputTokens", cfg.Embedding.MaxInputTokens, 4096},
		{"Embedding.Concurrency", cfg.Embedding.Concurrency, 8},
		{"Preprocessing.Provider", cfg.Preprocessing.Provider, "azure-openai"},
		{"Preprocessing.Model", cfg.Preprocessing.Model, "env-chat-model"},
		{"Preprocessing.BaseURL", cfg.Preprocessing.BaseURL, "http://env-chat/v1"},
		{"Preprocessing.Concurrency", cfg.Preprocessing.Concurrency, 6},
		{"Embedding.AzureEndpoint", cfg.Embedding.AzureEndpoint, "https://env.openai.azure.com"},
		{"Embedding.AzureDeployment", cfg.Embedding.AzureDeployment, "env-deployment"},
		{"Embedding.AzureAPIVersion", cfg.Embedding.AzureAPIVersion, "2025-01-01"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadInvalidIntEnvVarIgnored(t *testing.T) {
	useConfigFile(t)
	t.Setenv("EMBEDDING_DIMENSIONS", "not-a-number")
	t.Setenv("EMBEDDING_CONCURRENCY", "also-not-a-number")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Dimensions != 768 {
		t.Errorf("Embedding.Dimensions = %d, want default 768 preserved when env var is unparsable", cfg.Embedding.Dimensions)
	}
	if cfg.Embedding.Concurrency != 4 {
		t.Errorf("Embedding.Concurrency = %d, want default 4 preserved when env var is unparsable", cfg.Embedding.Concurrency)
	}
}

func TestLoadQdrantHTTPSParsing(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true}, {"TRUE", true}, {"1", true}, {"yes", true}, {"YES", true},
		{"false", false}, {"0", false}, {"no", false}, {"garbage", false},
	}
	for _, c := range cases {
		useConfigFile(t)
		t.Setenv("QDRANT_HTTPS", c.in)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Qdrant.HTTPS != c.want {
			t.Errorf("QDRANT_HTTPS=%q => HTTPS = %v, want %v", c.in, cfg.Qdrant.HTTPS, c.want)
		}
	}
}

func TestLoadDataDirCreatesAndRelocatesRelativeSourcesPath(t *testing.T) {
	useConfigFile(t)
	dataDir := filepath.Join(t.TempDir(), "nested", "data")
	t.Setenv("CONDUIT_DATA_DIR", dataDir)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("CONDUIT_DATA_DIR was not created: %v", err)
	}
	want := filepath.Join(dataDir, "conduit-sources.json")
	if cfg.SourcesFilePath != want {
		t.Errorf("SourcesFilePath = %q, want %q", cfg.SourcesFilePath, want)
	}
}

func TestLoadDataDirDoesNotOverrideAbsoluteSourcesPath(t *testing.T) {
	path := useConfigFile(t)
	absPath := filepath.Join(t.TempDir(), "elsewhere", "my-sources.json")
	encoded, err := json.Marshal(absPath)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"sources_file_path":` + string(encoded) + `}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUIT_DATA_DIR", filepath.Join(t.TempDir(), "data"))

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcesFilePath != absPath {
		t.Errorf("SourcesFilePath = %q, want unchanged absolute path %q", cfg.SourcesFilePath, absPath)
	}
}

func TestDataDirHelper(t *testing.T) {
	cfg := &AppConfig{SourcesFilePath: filepath.Join("some", "dir", "conduit-sources.json")}
	if got, want := DataDir(cfg), filepath.Join("some", "dir"); got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
}

func TestPathReflectsConduitConfigEnvVar(t *testing.T) {
	path := useConfigFile(t)
	if got := Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
}

func TestPathDefaultsWhenEnvVarUnset(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG", "")
	if got := Path(); got != "config.json" {
		t.Errorf("Path() = %q, want default %q", got, "config.json")
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	useConfigFile(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embedding.Model = "round-trip-model"
	cfg.Embedding.Concurrency = 12
	cfg.Qdrant.Host = "round-trip-host"

	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Embedding.Model != "round-trip-model" {
		t.Errorf("Embedding.Model = %q after round-trip, want %q", reloaded.Embedding.Model, "round-trip-model")
	}
	if reloaded.Embedding.Concurrency != 12 {
		t.Errorf("Embedding.Concurrency = %d after round-trip, want 12", reloaded.Embedding.Concurrency)
	}
	if reloaded.Qdrant.Host != "round-trip-host" {
		t.Errorf("Qdrant.Host = %q after round-trip, want %q", reloaded.Qdrant.Host, "round-trip-host")
	}
}

func TestSaveOverwritesExistingFile(t *testing.T) {
	path := useConfigFile(t)
	if err := os.WriteFile(path, []byte(`{"embedding":{"model":"old-model"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Model != "old-model" {
		t.Fatalf("precondition failed: Embedding.Model = %q", cfg.Embedding.Model)
	}
	cfg.Embedding.Model = "new-model"
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Embedding.Model != "new-model" {
		t.Errorf("Embedding.Model = %q after Save, want %q", reloaded.Embedding.Model, "new-model")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected .tmp file to be renamed away, got stat err = %v", err)
	}
}
