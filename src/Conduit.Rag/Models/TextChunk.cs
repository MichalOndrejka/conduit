namespace Conduit.Rag.Models;

public record TextChunk(
    string Text,
    int Index,
    int StartOffset,
    int EndOffset
);
