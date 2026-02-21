using Conduit.Rag.Models;

namespace Conduit.Rag.Sources;

public interface ISource
{
    string Type { get; }
    string CollectionName { get; }
    Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(CancellationToken ct = default);
}
