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
	cfg.Preprocessing.Enabled = true
	cfg.Preprocessing.BaseURL = srv.URL
	cfg.Preprocessing.Model = "llama3.2:3b"
	cfg.Preprocessing.Concurrency = concurrency
	return NewDocumentPreprocessor(cfg)
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

// TestPreprocessDisabledByDefault asserts EnabledForType is false when the
// master switch is off, even for a source type with no explicit override.
func TestPreprocessDisabledByDefault(t *testing.T) {
	p := NewDocumentPreprocessor(&config.AppConfig{})
	if p.EnabledForType("documentation") {
		t.Error("EnabledForType(\"documentation\") = true, want false when Preprocessing.Enabled is unset")
	}
}

// TestEnabledForTypeRespectsPerTypeOverride asserts a source type explicitly
// disabled in the map stays off, while one absent from the map defaults on.
func TestEnabledForTypeRespectsPerTypeOverride(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Preprocessing.Enabled = true
	cfg.Preprocessing.SourceTypes = map[string]bool{"code": false}
	p := NewDocumentPreprocessor(cfg)

	if p.EnabledForType("code") {
		t.Error("EnabledForType(\"code\") = true, want false (explicitly disabled)")
	}
	if !p.EnabledForType("documentation") {
		t.Error("EnabledForType(\"documentation\") = false, want true (absent from map defaults on)")
	}
}

// TestPreprocessCheckpointAbortsBeforeDispatch asserts a failing Checkpoint
// hook stops the run without summarizing any document.
func TestPreprocessCheckpointAbortsBeforeDispatch(t *testing.T) {
	var calls int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "summary"}}},
		})
	}
	p := testPreprocessor(t, handler, 4)

	docs := []models.SourceDocument{{ID: "doc-1", Text: strings.Repeat("word ", 100)}}
	wantErr := fmt.Errorf("sync cancelled")
	opts := PreprocessOptions{Checkpoint: func() error { return wantErr }}

	_, err := p.Preprocess(context.Background(), docs, "documentation", opts)
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Errorf("chat endpoint called %d times, want 0", got)
	}
}

// TestSummarizeFallsBackToOriginalOnErrors covers the "keep original text"
// degrade-gracefully paths: HTTP error status, unparseable JSON, and an
// empty summary.
func TestSummarizeFallsBackToOriginalOnErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "http error status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("boom"))
			},
		},
		{
			name: "unparseable response",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("not json"))
			},
		},
		{
			name: "no choices",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{}})
			},
		},
		{
			name: "empty summary",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]any{"content": "   "}}},
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testPreprocessor(t, tt.handler, 1)
			longText := strings.Repeat("word ", 100)
			docs := []models.SourceDocument{{ID: "doc-1", Text: longText}}
			out, err := p.Preprocess(context.Background(), docs, "documentation", PreprocessOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if out[0].Text != longText {
				t.Errorf("Text = %q, want original text preserved on failure", out[0].Text)
			}
		})
	}
}

func TestVerifySucceeds(t *testing.T) {
	p := testPreprocessor(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "OK"}}},
		})
	}, 1)

	msg, err := p.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "reachable") {
		t.Errorf("msg = %q", msg)
	}
}

func TestVerifyFailsWhenModelMissing(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Preprocessing.BaseURL = "http://localhost:1"
	p := NewDocumentPreprocessor(cfg)

	if _, err := p.Verify(context.Background()); err == nil {
		t.Error("expected error when model is unset")
	}
}

func TestVerifyFailsOnHTTPError(t *testing.T) {
	p := testPreprocessor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad key"))
	}, 1)

	_, err := p.Verify(context.Background())
	if err == nil {
		t.Fatal("expected error on HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want it to mention HTTP 401", err)
	}
}

func TestVerifyFailsOnUnreachableServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use — connection refused

	cfg := &config.AppConfig{}
	cfg.Preprocessing.BaseURL = srv.URL
	cfg.Preprocessing.Model = "llama3.2:3b"
	p := NewDocumentPreprocessor(cfg)

	if _, err := p.Verify(context.Background()); err == nil {
		t.Error("expected error from unreachable server")
	}
}
