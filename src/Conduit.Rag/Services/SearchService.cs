using Conduit.Rag.Extensions;
using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public sealed class SearchService(
    IVectorStore store,
    IEmbeddingService embeddings,
    IQdrantFilterFactory filters) : ISearchService
{
    public async Task<IReadOnlyList<SearchResult>> SearchAsync(
        string collectionName,
        string query,
        int topK = 5,
        Dictionary<string, string>? tags = null,
        CancellationToken ct = default)
    {
        var vector = await embeddings.EmbedAsync(query, ct);
        var filter = filters.CreateGrpcFilter(tags);

        var hits = await store.SearchAsync(
            collectionName: collectionName,
            vector:         vector,
            limit:          (ulong)topK,
            filter:         filter,
            withPayload:    true,
            ct:             ct);

        return hits.ToSearchResults();
    }
}
