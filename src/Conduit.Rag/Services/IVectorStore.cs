using Qdrant.Client.Grpc;

namespace Conduit.Rag.Services;

/// <summary>Abstraction over the vector database — enables unit testing of indexer and search services.</summary>
public interface IVectorStore
{
    Task UpsertAsync(
        string collectionName,
        IReadOnlyList<PointStruct> points,
        bool wait = true,
        CancellationToken ct = default);

    Task<IReadOnlyList<ScoredPoint>> SearchAsync(
        string collectionName,
        float[] vector,
        ulong limit,
        Filter? filter = null,
        bool withPayload = true,
        CancellationToken ct = default);

    Task<(IReadOnlyList<RetrievedPoint> Points, PointId? NextOffset)> ScrollAsync(
        string collectionName,
        Filter? filter = null,
        ulong limit = 20,
        PointId? offset = null,
        CancellationToken ct = default);
}
