// Document preprocessing — Go port of app/rag/preprocessor.py. Optionally runs
// each fetched document through an OpenAI-compatible chat model (Ollama, etc.)
// to summarize it before chunking, reducing token usage and noise. Failures
// degrade gracefully: the original text is kept, never dropped.
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
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

// minPreprocessLength skips summarizing very short documents — they're already
// concise and a round-trip would add latency for no benefit.
const minPreprocessLength = 200

const defaultPreprocessPrompt = "You are a technical documentation assistant. " +
	"Summarize the following document as concisely as possible while preserving " +
	"all key technical facts, identifiers, error codes, version numbers, and " +
	"procedure steps. Respond with only the summary — no preamble, no commentary."

type DocumentPreprocessor struct {
	enabled      bool
	model        string
	systemPrompt string
	sourceTypes  map[string]bool
	url          string
	httpClient   *http.Client
	concurrency  int
}

// PreprocessOptions carries the per-document progress and cancellation hooks,
// mirroring the indexer's IndexBatchOptions.
type PreprocessOptions struct {
	ProgressCb func(current, total int)
	Checkpoint func() error // return non-nil (e.g. ErrSyncCancelled) to abort
}

// NewDocumentPreprocessor builds a preprocessor from the current config. It is
// cheap to construct, so callers can build it per sync to pick up live config
// changes (preprocessing is not captured at startup like the embedding client).
func NewDocumentPreprocessor(cfg *config.AppConfig) *DocumentPreprocessor {
	pc := cfg.Preprocessing
	prompt := strings.TrimSpace(pc.SystemPrompt)
	if prompt == "" {
		prompt = defaultPreprocessPrompt
	}
	concurrency := effectiveConcurrency(pc.Concurrency)
	base := pc.BaseURL
	if base == "" {
		base = "http://localhost:11434/v1"
	}
	return &DocumentPreprocessor{
		enabled:      pc.Enabled,
		model:        pc.Model,
		systemPrompt: prompt,
		sourceTypes:  pc.SourceTypes,
		url:          strings.TrimRight(base, "/") + "/chat/completions",
		httpClient:   &http.Client{Timeout: 120 * time.Second, Transport: pooledTransport(concurrency)},
		concurrency:  concurrency,
	}
}

// EnabledForType reports whether preprocessing should run for a source type.
// A type absent from the map defaults to on, matching the Python behavior.
func (p *DocumentPreprocessor) EnabledForType(sourceType string) bool {
	if !p.enabled {
		return false
	}
	if v, ok := p.sourceTypes[sourceType]; ok {
		return v
	}
	return true
}

// Preprocess summarizes each document. Short documents pass through untouched.
// The only error returned is from the Checkpoint hook (cancellation); model
// failures keep the original text.
func (p *DocumentPreprocessor) Preprocess(
	ctx context.Context, docs []models.SourceDocument, sourceType string, opts PreprocessOptions,
) ([]models.SourceDocument, error) {
	if !p.EnabledForType(sourceType) {
		return docs, nil
	}

	out := make([]models.SourceDocument, len(docs))
	var (
		wg   sync.WaitGroup
		sem  = make(chan struct{}, p.concurrency)
		mu   sync.Mutex
		done int
	)

	for i, doc := range docs {
		if opts.Checkpoint != nil {
			if err := opts.Checkpoint(); err != nil {
				wg.Wait()
				return nil, err
			}
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, doc models.SourceDocument) {
			defer wg.Done()
			defer func() { <-sem }()

			if len(doc.Text) >= minPreprocessLength {
				doc.Text = p.summarize(ctx, doc)
			}
			out[i] = doc

			mu.Lock()
			done++
			if opts.ProgressCb != nil {
				opts.ProgressCb(done, len(docs))
			}
			mu.Unlock()
		}(i, doc)
	}
	wg.Wait()
	return out, nil
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (p *DocumentPreprocessor) summarize(ctx context.Context, doc models.SourceDocument) string {
	body, err := json.Marshal(map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "system", "content": p.systemPrompt},
			{"role": "user", "content": doc.Text},
		},
		"stream": false,
	})
	if err != nil {
		return doc.Text
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return doc.Text
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ollama") // matches embedding's local-key convention

	resp, err := p.httpClient.Do(req)
	if err != nil {
		log.Printf("warning: preprocessing call failed for doc %s — keeping original: %v", doc.ID, err)
		return doc.Text
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("warning: preprocessing HTTP %d for doc %s — keeping original", resp.StatusCode, doc.ID)
		return doc.Text
	}
	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Choices) == 0 {
		log.Printf("warning: unparseable preprocessing response for doc %s — keeping original", doc.ID)
		return doc.Text
	}
	summary := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if summary == "" {
		log.Printf("warning: empty summary for doc %s — keeping original", doc.ID)
		return doc.Text
	}
	return summary
}

// Verify probes the configured chat endpoint with a minimal request, for the
// Settings page's "Verify" button. It checks connectivity without depending
// on a model actually producing a useful reply.
func (p *DocumentPreprocessor) Verify(ctx context.Context) (string, error) {
	if strings.TrimSpace(p.model) == "" {
		return "", fmt.Errorf("model is required")
	}
	body, err := json.Marshal(map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "user", "content": "This is a connectivity test. Reply with OK."},
		},
		"stream": false,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ollama") // matches embedding's local-key convention

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, p.url, string(data))
	}
	return "OK — chat endpoint reachable", nil
}
