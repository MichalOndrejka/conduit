using Qdrant.Client;
using Qdrant.Client.Grpc;
using Qdrant.Demo.Api.Extensions;
using Qdrant.Demo.Api.Models;
using static Qdrant.Demo.Api.Models.PayloadKeys;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Production implementation of <see cref="IDocumentIndexer"/>.
/// Embeds the document text via OpenAI, stores tags as <c>tag_{key}</c>
/// and properties as <c>prop_{key}</c> in the Qdrant payload.
/// <para>
/// When the input text exceeds the configured chunk size, the text is
/// automatically split into overlapping chunks and each chunk is stored
/// as a separate Qdrant point with parent-child metadata.
/// </para>
/// </summary>
public sealed class DocumentIndexer(
    QdrantClient qdrant,
    IEmbeddingService embeddings,
    ITextChunker chunker,
    string collectionName) : IDocumentIndexer
{
    /// <inheritdoc />
    public async Task<DocumentUpsertResponse> IndexAsync(
        DocumentUpsertRequest request,
        CancellationToken ct = default)
    {
        // Deterministic source-id: from caller-supplied Id, or hash of Text
        var idSource = !string.IsNullOrWhiteSpace(request.Id)
            ? request.Id!
            : request.Text;
        var sourceId = idSource.ToDeterministicGuid().ToString("D");

        // Split into chunks (returns a single chunk if text is short enough)
        var chunks = chunker.Chunk(request.Text);

        List<PointStruct> points = [];
        List<string> chunkPointIds = [];

        for (var i = 0; i < chunks.Count; i++)
        {
            var chunk = chunks[i];

            // For single-chunk documents keep the original id;
            // for multi-chunk, append _chunk_{index} for uniqueness.
            var pointIdStr = chunks.Count == 1
                ? sourceId
                : $"{sourceId}_chunk_{i}".ToDeterministicGuid().ToString("D");

            chunkPointIds.Add(pointIdStr);

            // Generate embedding for this chunk's text
            var vector = await embeddings.EmbedAsync(chunk.Text, ct);

            var point = new PointStruct
            {
                Id = new PointId { Uuid = pointIdStr },
                Vectors = vector,
                Payload =
                {
                    [Text] = chunk.Text,
                    [IndexedAtMs] = DateTime.UtcNow.ToUnixMs()
                }
            };

            // Chunking metadata — always present so search results can
            // be grouped or filtered by source document.
            if (chunks.Count > 1)
            {
                point.Payload[SourceDocId] = sourceId;
                point.Payload[ChunkIndex] = i.ToString();
                point.Payload[TotalChunks] = chunks.Count.ToString();
            }

            // Store tags as tag_{key} — these are indexed and filterable.
            // Every chunk inherits the parent document's tags so that
            // tag-filtered searches still match.
            if (request.Tags is not null)
            {
                foreach (var (key, value) in request.Tags)
                    point.Payload[$"{TagPrefix}{key}"] = value;
            }

            // Store properties as prop_{key} — informational, not indexed.
            if (request.Properties is not null)
            {
                foreach (var (key, value) in request.Properties)
                    point.Payload[$"{PropertyPrefix}{key}"] = value;
            }

            points.Add(point);
        }

        await qdrant.UpsertAsync(
            collectionName,
            points,
            wait: true,
            cancellationToken: ct);

        return new DocumentUpsertResponse(
            PointId: chunkPointIds[0],
            TotalChunks: chunks.Count,
            ChunkPointIds: chunkPointIds);
    }
}
