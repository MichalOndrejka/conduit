// Embedding client — Go port of app/rag/embedding.py.
//
// Talks the OpenAI embeddings wire format directly (it is a single POST), so
// no SDK dependency is needed. Supports the same two providers as the Python
// app: "openai-compatible" (Ollama, etc.) and "azure-openai".
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// Conservative chars-per-token estimate. Real ratio is 3–4 for English,
// 2–3 for code. Using 2 keeps us safely below the token limit. Must stay in
// sync with chunker.go.
const charsPerToken = 2

// EmbeddingService reads its configuration from *config.AppConfig on every
// call, so embedding model changes made via the Settings page take effect on
// the next sync without requiring a restart.
type EmbeddingService struct {
	cfg        *config.AppConfig
	secrets    secrets.Reader
	httpClient *http.Client
}

func NewEmbeddingService(cfg *config.AppConfig, store secrets.Reader) *EmbeddingService {
	return &EmbeddingService{
		cfg:        cfg,
		secrets:    store,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (s *EmbeddingService) isAzure() bool {
	return s.cfg.Embedding.Provider == "azure-openai"
}

func (s *EmbeddingService) embeddingURL() string {
	ec := s.cfg.Embedding
	if ec.Provider == "azure-openai" {
		model := ec.AzureDeployment
		if model == "" {
			model = ec.Model
		}
		return fmt.Sprintf(
			"%s/openai/deployments/%s/embeddings?api-version=%s",
			strings.TrimRight(ec.AzureEndpoint, "/"), model, ec.AzureAPIVersion,
		)
	}
	return strings.TrimRight(ec.BaseURL, "/") + "/embeddings"
}

func (s *EmbeddingService) embeddingModel() string {
	ec := s.cfg.Embedding
	if ec.Provider == "azure-openai" && ec.AzureDeployment != "" {
		return ec.AzureDeployment
	}
	return ec.Model
}

// apiKey resolves the credential at call time, not construction time.
func (s *EmbeddingService) apiKey() string {
	ec := s.cfg.Embedding
	if ec.Provider != "azure-openai" {
		return "ollama" // matches Python: api_key="ollama"
	}
	if ec.AzureAPIKeyCredential != "" && s.secrets != nil {
		if key := s.secrets.GetValue(ec.AzureAPIKeyCredential); key != "" {
			return key
		}
	}
	return os.Getenv("AZURE_OPENAI_API_KEY")
}

// Embed returns the embedding vector for text, truncating over-long input the
// same way app/rag/embedding.py does.
func (s *EmbeddingService) Embed(ctx context.Context, text string) ([]float32, error) {
	ec := s.cfg.Embedding
	maxChars := ec.MaxInputTokens * charsPerToken

	if len(text) > maxChars {
		log.Printf(
			"warning: truncating input from %d to %d chars before embedding (model limit: %d tokens × %d chars/token estimate)",
			len(text), maxChars, ec.MaxInputTokens, charsPerToken,
		)
		// Back the cut off to a rune boundary — Go slices bytes, and cutting
		// a multi-byte UTF-8 rune in half would send invalid UTF-8 downstream.
		cut := maxChars
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		truncated := text[:cut]
		if lastNL := strings.LastIndex(truncated, "\n"); lastNL > maxChars/2 {
			truncated = truncated[:lastNL]
		}
		text = truncated
	}

	reqBody, err := json.Marshal(map[string]any{"model": s.embeddingModel(), "input": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.embeddingURL(), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.isAzure() {
		req.Header.Set("api-key", s.apiKey())
	} else {
		req.Header.Set("Authorization", "Bearer "+s.apiKey())
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding API: HTTP %d: %s", resp.StatusCode, Truncate(string(data), 300))
	}

	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("embedding API returned no data")
	}
	return parsed.Data[0].Embedding, nil
}
