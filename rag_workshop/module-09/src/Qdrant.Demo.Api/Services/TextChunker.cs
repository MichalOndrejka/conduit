using Qdrant.Demo.Api.Models;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Character-based text chunker with sentence-boundary awareness.
/// <para>
/// Splits text into overlapping chunks of at most
/// <see cref="ChunkingOptions.MaxChunkSize"/> characters, trying to break
/// at sentence boundaries (<c>. </c>, <c>? </c>, <c>! </c>, or newlines)
/// so that no sentence is cut mid-way.
/// </para>
/// </summary>
public sealed class TextChunker(ChunkingOptions options) : ITextChunker
{
    // Sentence-ending characters followed by whitespace.
    private static readonly char[] SentenceEnders = ['.', '?', '!'];

    /// <inheritdoc />
    public IReadOnlyList<TextChunk> Chunk(string text)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(text);

        // If text fits in a single chunk, return it as-is.
        if (text.Length <= options.MaxChunkSize)
        {
            return [new TextChunk(text, Index: 0, StartOffset: 0, EndOffset: text.Length)];
        }

        List<TextChunk> chunks = [];
        var chunkIndex = 0;
        var start = 0;

        while (start < text.Length)
        {
            var remaining = text.Length - start;
            var length = Math.Min(options.MaxChunkSize, remaining);

            // Not the last chunk? Try to find a sentence boundary to break at.
            if (start + length < text.Length)
            {
                length = FindSentenceBoundary(text, start, length);
            }

            var chunkText = text.Substring(start, length).Trim();

            if (chunkText.Length > 0)
            {
                chunks.Add(new TextChunk(chunkText, chunkIndex, start, start + length));
                chunkIndex++;
            }

            // Advance by (chunkLength - overlap) so the next chunk
            // repeats the last `Overlap` characters for context continuity.
            var advance = length - options.Overlap;

            // Guard: always advance at least 1 character to avoid infinite loops.
            if (advance < 1) advance = length;

            start += advance;
        }

        return chunks;
    }

    /// <summary>
    /// Scans backwards from the end of the proposed chunk to find the last
    /// sentence boundary. Returns the adjusted length.
    /// </summary>
    private static int FindSentenceBoundary(string text, int start, int maxLength)
    {
        // Don't look further back than half the chunk — if no boundary is
        // found in the second half, just cut at the max length.
        var searchStart = start + maxLength / 2;

        // Prefer paragraph breaks first.
        var newlinePos = text.LastIndexOf('\n', start + maxLength - 1, maxLength - (searchStart - start));
        if (newlinePos > searchStart)
            return newlinePos - start + 1; // include the newline

        // Then try sentence enders followed by a space.
        for (var i = start + maxLength - 1; i >= searchStart; i--)
        {
            if (Array.IndexOf(SentenceEnders, text[i]) >= 0
                && i + 1 < text.Length
                && char.IsWhiteSpace(text[i + 1]))
            {
                return i - start + 1; // include the punctuation
            }
        }

        // Last resort: break at any whitespace.
        var spacePos = text.LastIndexOf(' ', start + maxLength - 1, maxLength - (searchStart - start));
        if (spacePos > searchStart)
            return spacePos - start;

        // No good boundary found — hard cut.
        return maxLength;
    }
}
