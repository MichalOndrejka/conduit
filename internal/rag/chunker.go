// Text chunker — Go port of app/rag/chunker.py.
package rag

import (
	"log"
	"regexp"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

var (
	sentenceEnd = regexp.MustCompile(`[.!?]\s+`)
	newline     = regexp.MustCompile(`\n`)
)

type TextChunker struct {
	maxSize int
	overlap int
}

func NewTextChunker(cfg *config.AppConfig) *TextChunker {
	c := &TextChunker{
		maxSize: cfg.Chunking.MaxChunkSize,
		overlap: cfg.Chunking.Overlap,
	}
	effectiveEmbedChars := cfg.Embedding.MaxInputTokens * charsPerToken
	if c.maxSize > effectiveEmbedChars {
		log.Printf(
			"warning: max_chunk_size (%d chars) exceeds embedding model limit (%d tokens × %d chars/token = %d chars) — clamping chunk size",
			c.maxSize, cfg.Embedding.MaxInputTokens, charsPerToken, effectiveEmbedChars,
		)
		c.maxSize = effectiveEmbedChars
	}
	return c
}

// Chunk splits text into overlapping chunks no larger than maxSize characters.
//
// Break priority (same as the Python implementation):
//  1. Last sentence boundary ([.!?] followed by whitespace) inside the window
//  2. Last newline inside the window — natural for code
//  3. Hard character cut — absolute fallback
func (c *TextChunker) Chunk(text string) []models.TextChunk {
	if text == "" {
		return nil
	}

	// Fast path: fits in one chunk
	if len(text) <= c.maxSize {
		return []models.TextChunk{{Text: text, Index: 0, StartOffset: 0, EndOffset: len(text)}}
	}

	var chunks []models.TextChunk
	start := 0
	idx := 0

	for start < len(text) {
		end := min(start+c.maxSize, len(text))

		if end < len(text) {
			window := text[start:end]

			// Priority 1: last sentence boundary
			bestBreak := -1
			for _, m := range sentenceEnd.FindAllStringIndex(window, -1) {
				bestBreak = m[1]
			}

			// Priority 2: last newline (crucial for code — keeps lines intact)
			if bestBreak < 0 {
				for _, m := range newline.FindAllStringIndex(window, -1) {
					bestBreak = m[1]
				}
			}

			// Reject a break too close to the start (< 25% of window) to
			// avoid tiny slivers that stall progress.
			minBreak := max(1, c.maxSize/4)
			if bestBreak >= 0 && bestBreak < minBreak {
				bestBreak = -1 // fall through to hard cut
			}

			if bestBreak >= 0 {
				end = start + bestBreak
			}
		}

		chunkText := strings.TrimSpace(text[start:end])
		if chunkText != "" {
			chunks = append(chunks, models.TextChunk{
				Text:        chunkText,
				Index:       idx,
				StartOffset: start,
				EndOffset:   end,
			})
			idx++
		}

		if end >= len(text) {
			break
		}

		// Advance with overlap — always move forward by at least 1 char
		start = max(start+1, end-c.overlap)
	}

	return chunks
}
