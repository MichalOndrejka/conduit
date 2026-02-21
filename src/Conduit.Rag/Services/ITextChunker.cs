using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public interface ITextChunker
{
    IReadOnlyList<TextChunk> Chunk(string text);
}
