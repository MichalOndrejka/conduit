package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

func testPreprocessor(t *testing.T, chatHandler http.HandlerFunc, concurrency int) *DocumentPreprocessor {
	t.Helper()
	srv := httptest.NewServer(chatHandler)
	t.Cleanup(srv.Close)

	cfg := &config.AppConfig{}
	cfg.Preprocessing.Provider = "openai-compatible"
	cfg.Preprocessing.BaseURL = srv.URL
	cfg.Preprocessing.Model = "llama3.2:3b"
	cfg.Preprocessing.Concurrency = concurrency
	return NewDocumentPreprocessor(cfg, nil)
}

// TestPreprocessSummarizesConcurrently asserts documents are summarized in
// parallel, not one blocking chat call at a time.
func TestPreprocessSummarizesConcurrently(t *testing.T) {
	var inFlight, peak int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&inFlight, 1)
		defer atomic.AddInt64(&inFlight, -1)
		for {
			p := atomic.LoadInt64(&peak)
			if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "summary"}}},
		})
	}

	const concurrency = 4
	p := testPreprocessor(t, handler, concurrency)

	docs := make([]models.SourceDocument, 8)
	for i := range docs {
		docs[i] = models.SourceDocument{ID: fmt.Sprintf("doc-%d", i), Text: strings.Repeat("word ", 100)}
	}

	start := time.Now()
	out, err := p.Preprocess(context.Background(), docs, "documentation", PreprocessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if got := atomic.LoadInt64(&peak); got < 2 {
		t.Errorf("peak concurrent summarize calls = %d, want > 1 (sequential preprocessing not fixed)", got)
	}
	if got := atomic.LoadInt64(&peak); got > concurrency {
		t.Errorf("peak concurrent summarize calls = %d, want <= configured concurrency %d", got, concurrency)
	}
	if elapsed >= 160*time.Millisecond {
		t.Errorf("Preprocess took %v, expected concurrency to make this much faster than sequential", elapsed)
	}

	if len(out) != len(docs) {
		t.Fatalf("got %d docs, want %d", len(out), len(docs))
	}
	for i, d := range out {
		if d.ID != docs[i].ID {
			t.Errorf("out[%d].ID = %q, want %q — order not preserved", i, d.ID, docs[i].ID)
		}
		if d.Text != "summary" {
			t.Errorf("out[%d].Text = %q, want summarized text", i, d.Text)
		}
	}
}

// TestPreprocessSkipsShortDocuments asserts the skip-if-short rule still
// applies per-job under concurrency.
func TestPreprocessSkipsShortDocuments(t *testing.T) {
	var calls int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "summary"}}},
		})
	}
	p := testPreprocessor(t, handler, 4)

	docs := []models.SourceDocument{
		{ID: "short", Text: "tiny"},
		{ID: "long", Text: strings.Repeat("word ", 100)},
	}
	out, err := p.Preprocess(context.Background(), docs, "documentation", PreprocessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Text != "tiny" {
		t.Errorf("short doc text = %q, want unchanged %q", out[0].Text, "tiny")
	}
	if out[1].Text != "summary" {
		t.Errorf("long doc text = %q, want summarized", out[1].Text)
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Errorf("chat endpoint called %d times, want 1 (only the long doc)", got)
	}
}
