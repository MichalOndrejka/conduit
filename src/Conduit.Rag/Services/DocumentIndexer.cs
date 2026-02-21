using Conduit.Rag.Extensions;
using Conduit.Rag.Models;
using Qdrant.Client.Grpc;
using static Conduit.Rag.Models.PayloadKeys;

namespace Conduit.Rag.Services;

public sealed class DocumentIndexer(
    IVectorStore store,
    IEmbeddingService embeddings,
    ITextChunker chunker) : IDocumentIndexer
{
    public async Task IndexAsync(string collectionName, SourceDocument document, CancellationToken ct = default)
    {
        var sourceId = document.Id.ToDeterministicGuid().ToString("D");
        var chunks = chunker.Chunk(document.Text);
        var points = new List<PointStruct>(chunks.Count);

        for (var i = 0; i < chunks.Count; i++)
        {
            var chunk = chunks[i];

            var pointIdStr = chunks.Count == 1
                ? sourceId
                : $"{sourceId}_chunk_{i}".ToDeterministicGuid().ToString("D");

            var vector = await embeddings.EmbedAsync(chunk.Text, ct);

            var point = new PointStruct
            {
                Id = new PointId { Uuid = pointIdStr },
                Vectors = vector,
                Payload =
                {
                    [Text]        = chunk.Text,
                    [IndexedAtMs] = DateTime.UtcNow.ToUnixMs()
                }
            };

            if (chunks.Count > 1)
            {
                point.Payload[SourceDocId] = sourceId;
                point.Payload[ChunkIndex]  = i.ToString();
                point.Payload[TotalChunks] = chunks.Count.ToString();
            }

            foreach (var (key, value) in document.Tags)
                point.Payload[$"{TagPrefix}{key}"] = value;

            foreach (var (key, value) in document.Properties)
                point.Payload[$"{PropPrefix}{key}"] = value;

            points.Add(point);
        }

        await store.UpsertAsync(collectionName, points, wait: true, ct: ct);
    }

    public async Task IndexBatchAsync(string collectionName, IReadOnlyList<SourceDocument> documents, CancellationToken ct = default)
    {
        foreach (var document in documents)
            await IndexAsync(collectionName, document, ct);
    }
}
