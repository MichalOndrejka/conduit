using Qdrant.Demo.Api.Models;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Splits a text string into smaller, optionally overlapping chunks
/// that each fit within an embedding model's token limit.
/// </summary>
public interface ITextChunker
{
    /// <summary>
    /// Split <paramref name="text"/> into chunks.
    /// If the text is already short enough, a single chunk is returned.
    /// </summary>
    IReadOnlyList<TextChunk> Chunk(string text);
}
