package rag

import (
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
)

func chunkerWith(maxSize, overlap int) *TextChunker {
	cfg := &config.AppConfig{}
	cfg.Chunking.MaxChunkSize = maxSize
	cfg.Chunking.Overlap = overlap
	cfg.Embedding.MaxInputTokens = 8192
	return NewTextChunker(cfg)
}

func TestEmptyText(t *testing.T) {
	if got := chunkerWith(100, 10).Chunk(""); len(got) != 0 {
		t.Errorf("expected no chunks, got %d", len(got))
	}
}

func TestSingleChunkFastPath(t *testing.T) {
	chunks := chunkerWith(100, 10).Chunk("short text")
	if len(chunks) != 1 || chunks[0].Text != "short text" {
		t.Errorf("unexpected chunks: %+v", chunks)
	}
}

func TestNeverExceedsMaxSize(t *testing.T) {
	text := strings.Repeat("x", 5000) // no sentence breaks, no newlines
	for _, c := range chunkerWith(200, 20).Chunk(text) {
		if len(c.Text) > 200 {
			t.Errorf("chunk of %d chars exceeds max 200", len(c.Text))
		}
	}
}

func TestBreaksAtSentenceBoundary(t *testing.T) {
	text := strings.Repeat("This is a sentence. ", 50)
	for _, c := range chunkerWith(100, 10).Chunk(text) {
		if !strings.HasSuffix(c.Text, ".") {
			t.Errorf("chunk does not end at a sentence boundary: %q", c.Text)
		}
	}
}

func TestBreaksAtNewlineForCode(t *testing.T) {
	var b strings.Builder
	for range 60 {
		b.WriteString("func doSomething(arg int) int { return arg }\n")
	}
	for _, c := range chunkerWith(200, 20).Chunk(b.String()) {
		if strings.Contains(c.Text, "{ ret\nurn") {
			t.Errorf("line split mid-statement: %q", c.Text)
		}
		if len(c.Text) > 200 {
			t.Errorf("chunk exceeds max size: %d", len(c.Text))
		}
	}
}

func TestClampsToEmbeddingLimit(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Chunking.MaxChunkSize = 100000
	cfg.Chunking.Overlap = 10
	cfg.Embedding.MaxInputTokens = 100 // 100 tokens × 2 chars = 200 chars
	c := NewTextChunker(cfg)
	for _, ch := range c.Chunk(strings.Repeat("y", 1000)) {
		if len(ch.Text) > 200 {
			t.Errorf("chunk of %d chars exceeds clamped limit 200", len(ch.Text))
		}
	}
}

func TestChunkIndicesAreSequential(t *testing.T) {
	chunks := chunkerWith(100, 10).Chunk(strings.Repeat("Word after word. ", 100))
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has index %d", i, c.Index)
		}
	}
}
