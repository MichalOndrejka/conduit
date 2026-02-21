using Qdrant.Demo.Api.Models;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Embeds a document's text and upserts it into Qdrant.
/// </summary>
public interface IDocumentIndexer
{
    /// <summary>
    /// Generate an embedding for <paramref name="request"/>.Text,
    /// build a Qdrant point, and upsert it into the collection.
    /// </summary>
    Task<DocumentUpsertResponse> IndexAsync(
        DocumentUpsertRequest request,
        CancellationToken ct = default);
}
