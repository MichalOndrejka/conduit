namespace Conduit.Rag.Services;

public interface ISyncService
{
    Task SyncAsync(string sourceId, CancellationToken ct = default);
    Task SyncAllAsync(CancellationToken ct = default);
}
