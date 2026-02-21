using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public interface ISearchService
{
    Task<IReadOnlyList<SearchResult>> SearchAsync(
        string collectionName,
        string query,
        int topK = 5,
        Dictionary<string, string>? tags = null,
        CancellationToken ct = default);
}
