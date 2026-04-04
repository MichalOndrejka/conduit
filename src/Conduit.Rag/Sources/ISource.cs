using Conduit.Rag.Models;

namespace Conduit.Rag.Sources;

public interface ISource
{
    string Type { get; }
    string CollectionName { get; }
    Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(IProgress<string>? fetchProgress = null, CancellationToken ct = default);
}
