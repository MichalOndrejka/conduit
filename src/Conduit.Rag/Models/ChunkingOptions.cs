namespace Conduit.Rag.Models;

public sealed class ChunkingOptions
{
    /// <summary>Maximum characters per chunk (~500 tokens for English text).</summary>
    public int MaxChunkSize { get; set; } = 2000;

    /// <summary>Character overlap between consecutive chunks for context continuity.</summary>
    public int Overlap { get; set; } = 200;
}
