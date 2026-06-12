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

type EmbeddingService struct {
	url   string
	azure bool
	// Azure resolves the API key per request (azureCredential → secrets
	// store, falling back to AZURE_OPENAI_API_KEY) so rotating the
	// credential in the UI takes effect without a restart.
	azureCredential string
	secrets         secrets.Reader
	model           string
	maxTokens       int
	maxChars        int
	httpClient      *http.Client
}

func NewEmbeddingService(cfg *config.AppConfig, store secrets.Reader) *EmbeddingService {
	ec := cfg.Embedding
	s := &EmbeddingService{
		secrets:    store,
		maxTokens:  ec.MaxInputTokens,
		maxChars:   ec.MaxInputTokens * charsPerToken,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	if ec.Provider == "azure-openai" {
		s.azure = true
		s.azureCredential = ec.AzureAPIKeyCredential
		s.model = ec.AzureDeployment
		if s.model == "" {
			s.model = ec.Model
		}
		s.url = fmt.Sprintf(
			"%s/openai/deployments/%s/embeddings?api-version=%s",
			strings.TrimRight(ec.AzureEndpoint, "/"), s.model, ec.AzureAPIVersion,
		)
	} else {
		s.model = ec.Model
		s.url = strings.TrimRight(ec.BaseURL, "/") + "/embeddings"
	}
	return s
}

// apiKey resolves the credential at call time, not construction time.
func (s *EmbeddingService) apiKey() string {
	if !s.azure {
		return "ollama" // matches Python: api_key="ollama"
	}
	if s.azureCredential != "" && s.secrets != nil {
		if key := s.secrets.GetValue(s.azureCredential); key != "" {
			return key
		}
	}
	return os.Getenv("AZURE_OPENAI_API_KEY")
}

// Embed returns the embedding vector for text, truncating over-long input the
// same way app/rag/embedding.py does.
func (s *EmbeddingService) Embed(ctx context.Context, text string) ([]float32, error) {
	if len(text) > s.maxChars {
		log.Printf(
			"warning: truncating input from %d to %d chars before embedding (model limit: %d tokens × %d chars/token estimate)",
			len(text), s.maxChars, s.maxTokens, charsPerToken,
		)
		// Back the cut off to a rune boundary — Go slices bytes, and cutting
		// a multi-byte UTF-8 rune in half would send invalid UTF-8 downstream.
		cut := s.maxChars
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		truncated := text[:cut]
		if lastNL := strings.LastIndex(truncated, "\n"); lastNL > s.maxChars/2 {
			truncated = truncated[:lastNL]
		}
		text = truncated
	}

	reqBody, err := json.Marshal(map[string]any{"model": s.model, "input": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.azure {
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
