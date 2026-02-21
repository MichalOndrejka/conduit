namespace Qdrant.Demo.Api.Models;

/// <summary>
/// Configuration options for <see cref="Services.ITextChunker"/>.
/// </summary>
/// <remarks>
/// <para>
/// <b>Why character-based?</b>  OpenAI's <c>cl100k_base</c> tokenizer
/// averages ~4 characters per token for English text.  A character-based
/// approach avoids adding a tokenizer dependency while staying safely under
/// the model's 8,191-token limit.
/// </para>
/// <para>
/// If you need exact token counting, add the
/// <c>Microsoft.ML.Tokenizers</c> NuGet package and implement a
/// token-aware chunker — but for most workshop / prototype scenarios
/// the character approximation is good enough.
/// </para>
/// </remarks>
public sealed class ChunkingOptions
{
    /// <summary>
    /// Maximum number of characters per chunk.
    /// Default <c>2000</c> ≈ 500 tokens — well under the 8,191-token limit,
    /// leaving room for non-English text where the ratio is lower.
    /// </summary>
    public int MaxChunkSize { get; set; } = 2000;

    /// <summary>
    /// Number of characters duplicated between consecutive chunks to
    /// preserve context across boundaries.
    /// Default <c>200</c> ≈ 10% of the default chunk size.
    /// </summary>
    public int Overlap { get; set; } = 200;
}
