using Microsoft.Extensions.AI;

namespace Conduit.Rag.Services;

public sealed class EmbeddingService(
    IEmbeddingGenerator<string, Embedding<float>> generator) : IEmbeddingService
{
    public async Task<float[]> EmbedAsync(string text, CancellationToken ct = default)
    {
        var embedding = await generator.GenerateAsync([text], cancellationToken: ct);
        return embedding[0].Vector.ToArray();
    }
}
