namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Converts text into a vector embedding.
/// </summary>
public interface IEmbeddingService
{
    /// <summary>
    /// Generate a float-vector embedding for the supplied <paramref name="text"/>
    /// using the configured embedding model.
    /// </summary>
    Task<float[]> EmbedAsync(string text, CancellationToken ct = default);
}
