package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MichalOndrejka/conduit/internal/config"
)

func embedHandler(t *testing.T, gotReq *http.Request) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*gotReq = *r
		var body struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}
}

func TestOpenAICompatibleProvider(t *testing.T) {
	var got http.Request
	srv := httptest.NewServer(embedHandler(t, &got))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Embedding.Model = "nomic-embed-text"
	cfg.Embedding.BaseURL = srv.URL + "/v1"
	cfg.Embedding.MaxInputTokens = 8192

	svc := NewEmbeddingService(cfg)
	vec, err := svc.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 3 {
		t.Errorf("vector len = %d", len(vec))
	}
	if got.URL.Path != "/v1/embeddings" {
		t.Errorf("path = %q", got.URL.Path)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer ollama" {
		t.Errorf("auth header = %q", auth)
	}
}

func TestTruncatesOverlongInput(t *testing.T) {
	var capturedInput string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedInput = body.Input
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1}}},
		})
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = srv.URL
	cfg.Embedding.MaxInputTokens = 10 // 10 tokens × 2 chars = 20 chars max

	svc := NewEmbeddingService(cfg)
	if _, err := svc.Embed(context.Background(), strings.Repeat("a", 100)); err != nil {
		t.Fatal(err)
	}
	if len(capturedInput) > 20 {
		t.Errorf("input not truncated: %d chars sent", len(capturedInput))
	}
}

func TestTruncationRespectsRuneBoundaries(t *testing.T) {
	var capturedInput string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedInput = body.Input
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1}}},
		})
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = srv.URL
	cfg.Embedding.MaxInputTokens = 10 // 20-byte cap

	// "é" is 2 bytes; 21 of them puts the byte cut mid-rune at position 20.
	svc := NewEmbeddingService(cfg)
	if _, err := svc.Embed(context.Background(), strings.Repeat("é", 21)); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(capturedInput) {
		t.Errorf("truncation produced invalid UTF-8: %q", capturedInput)
	}
}
