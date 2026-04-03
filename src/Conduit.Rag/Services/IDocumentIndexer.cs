using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public interface IDocumentIndexer
{
    Task IndexAsync(string collectionName, SourceDocument document, CancellationToken ct = default);
    Task IndexBatchAsync(string collectionName, IReadOnlyList<SourceDocument> documents, IProgress<(int current, int total)>? progress = null, CancellationToken ct = default);
}
