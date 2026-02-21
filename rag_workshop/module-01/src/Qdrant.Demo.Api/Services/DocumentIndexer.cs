using Qdrant.Client;
using Qdrant.Client.Grpc;
using Qdrant.Demo.Api.Extensions;
using Qdrant.Demo.Api.Models;
using static Qdrant.Demo.Api.Models.PayloadKeys;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Production implementation of <see cref="IDocumentIndexer"/>.
/// Embeds the document text via OpenAI and stores it as a Qdrant point.
/// </summary>
public sealed class DocumentIndexer(
    QdrantClient qdrant,
    IEmbeddingService embeddings,
    string collectionName) : IDocumentIndexer
{
    /// <inheritdoc />
    public async Task<DocumentUpsertResponse> IndexAsync(
        DocumentUpsertRequest request,
        CancellationToken ct = default)
    {
        // Deterministic point-id: from caller-supplied Id, or hash of Text
        var idSource = !string.IsNullOrWhiteSpace(request.Id)
            ? request.Id!
            : request.Text;
        var pointId = idSource.ToDeterministicGuid().ToString("D");

        // Generate embedding for the text
        var vector = await embeddings.EmbedAsync(request.Text, ct);

        // Build the Qdrant point
        var point = new PointStruct
        {
            Id = new PointId { Uuid = pointId },
            Vectors = vector,
            Payload =
            {
                [Text] = request.Text,
                [IndexedAtMs] = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()
            }
        };

        // Upsert into Qdrant (idempotent — same point-id overwrites)
        await qdrant.UpsertAsync(collectionName, [point], wait: true, cancellationToken: ct);

        return new DocumentUpsertResponse(PointId: pointId);
    }
}
