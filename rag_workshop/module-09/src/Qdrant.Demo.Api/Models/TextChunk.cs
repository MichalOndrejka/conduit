namespace Qdrant.Demo.Api.Models;

/// <summary>
/// A single chunk produced by <see cref="Services.ITextChunker"/>.
/// </summary>
/// <param name="Text">The chunk content.</param>
/// <param name="Index">Zero-based chunk index within the source document.</param>
/// <param name="StartOffset">Character offset in the original text where this chunk begins.</param>
/// <param name="EndOffset">Character offset in the original text where this chunk ends (exclusive).</param>
public record TextChunk(
    string Text,
    int Index,
    int StartOffset,
    int EndOffset
);
