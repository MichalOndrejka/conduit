using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public sealed class TextChunker(ChunkingOptions options) : ITextChunker
{
    private static readonly char[] SentenceEnders = ['.', '?', '!'];

    public IReadOnlyList<TextChunk> Chunk(string text)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(text);

        if (text.Length <= options.MaxChunkSize)
            return [new TextChunk(text, Index: 0, StartOffset: 0, EndOffset: text.Length)];

        List<TextChunk> chunks = [];
        var chunkIndex = 0;
        var start = 0;

        while (start < text.Length)
        {
            var remaining = text.Length - start;
            var length = Math.Min(options.MaxChunkSize, remaining);

            if (start + length < text.Length)
                length = FindSentenceBoundary(text, start, length);

            var chunkText = text.Substring(start, length).Trim();

            if (chunkText.Length > 0)
            {
                chunks.Add(new TextChunk(chunkText, chunkIndex, start, start + length));
                chunkIndex++;
            }

            var advance = length - options.Overlap;
            if (advance < 1) advance = length;
            start += advance;
        }

        return chunks;
    }

    private static int FindSentenceBoundary(string text, int start, int maxLength)
    {
        var searchStart = start + maxLength / 2;

        var newlinePos = text.LastIndexOf('\n', start + maxLength - 1, maxLength - (searchStart - start));
        if (newlinePos > searchStart)
            return newlinePos - start + 1;

        for (var i = start + maxLength - 1; i >= searchStart; i--)
        {
            if (Array.IndexOf(SentenceEnders, text[i]) >= 0
                && i + 1 < text.Length
                && char.IsWhiteSpace(text[i + 1]))
            {
                return i - start + 1;
            }
        }

        var spacePos = text.LastIndexOf(' ', start + maxLength - 1, maxLength - (searchStart - start));
        if (spacePos > searchStart)
            return spacePos - start;

        return maxLength;
    }
}
