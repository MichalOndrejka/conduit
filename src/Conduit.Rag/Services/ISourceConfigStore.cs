using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public interface ISourceConfigStore
{
    Task<IReadOnlyList<SourceDefinition>> GetAllAsync(CancellationToken ct = default);
    Task<SourceDefinition?> GetByIdAsync(string id, CancellationToken ct = default);
    Task SaveAsync(SourceDefinition source, CancellationToken ct = default);
    Task DeleteAsync(string id, CancellationToken ct = default);
}
