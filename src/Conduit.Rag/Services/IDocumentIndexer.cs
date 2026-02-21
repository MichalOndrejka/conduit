using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public interface IDocumentIndexer
{
    Task IndexAsync(string collectionName, SourceDocument document, CancellationToken ct = default);
    Task IndexBatchAsync(string collectionName, IReadOnlyList<SourceDocument> documents, CancellationToken ct = default);
}
