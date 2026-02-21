using Qdrant.Client;
using Qdrant.Client.Grpc;

namespace Conduit.Rag.Services;

public sealed class QdrantVectorStore(QdrantClient qdrant) : IVectorStore
{
    public async Task UpsertAsync(
        string collectionName,
        IReadOnlyList<PointStruct> points,
        bool wait = true,
        CancellationToken ct = default)
        => await qdrant.UpsertAsync(collectionName, points.ToList(), wait, cancellationToken: ct);

    public async Task<IReadOnlyList<ScoredPoint>> SearchAsync(
        string collectionName,
        float[] vector,
        ulong limit,
        Filter? filter = null,
        bool withPayload = true,
        CancellationToken ct = default)
        => await qdrant.SearchAsync(
            collectionName:    collectionName,
            vector:            vector,
            limit:             limit,
            filter:            filter,
            payloadSelector:   withPayload,
            cancellationToken: ct);
}
