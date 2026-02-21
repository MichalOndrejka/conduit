using Microsoft.Extensions.AI;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Production implementation of <see cref="IEmbeddingService"/>
/// backed by OpenAI via Microsoft.Extensions.AI.
/// </summary>
public sealed class EmbeddingService(
    IEmbeddingGenerator<string, Embedding<float>> generator) : IEmbeddingService
{
    /// <inheritdoc />
    public async Task<float[]> EmbedAsync(string text, CancellationToken ct = default)
    {
        var embedding = await generator.GenerateAsync(
            [text], cancellationToken: ct);
        return embedding[0].Vector.ToArray();
    }
}
