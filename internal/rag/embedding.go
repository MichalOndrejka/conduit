// Embedding client — Go port of app/rag/embedding.py.
//
// Talks the OpenAI embeddings wire format directly (it is a single POST), so
// no SDK dependency is needed. Local / OpenAI-compatible only (Ollama, etc.).
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MichalOndrejka/conduit/internal/config"
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
	httpClient *http.Client
}

func NewEmbeddingService(cfg *config.AppConfig) *EmbeddingService {
	return &EmbeddingService{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 60 * time.Second, Transport: pooledTransport(effectiveConcurrency(cfg.Embedding.Concurrency))},
	}
}

// effectiveConcurrency floors an unset/invalid concurrency config value to 1
// so a worker pool's semaphore channel is never sized 0 (which would deadlock
// forever on the first send).
func effectiveConcurrency(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// pooledTransport clones http.DefaultTransport with a higher per-host idle
// connection cap so concurrent embed/preprocess calls to the same Ollama
// host reuse keep-alive connections instead of each opening a new one — the
// default cap is 2, which would otherwise bottleneck any concurrency > 2.
func pooledTransport(concurrency int) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	maxConns := concurrency * 2
	if maxConns < 10 {
		maxConns = 10
	}
	t.MaxIdleConnsPerHost = maxConns
	t.MaxIdleConns = maxConns
	return t
}

// concurrency returns the configured max in-flight embed calls, read live so
// a Settings-page change takes effect on the next sync.
func (s *EmbeddingService) concurrency() int {
	return effectiveConcurrency(s.cfg.Embedding.Concurrency)
}

func (s *EmbeddingService) embeddingURL() string {
	return strings.TrimRight(s.cfg.Embedding.BaseURL, "/") + "/embeddings"
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

	reqBody, err := json.Marshal(map[string]any{"model": ec.Model, "input": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.embeddingURL(), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ollama") // matches Python: api_key="ollama"

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
