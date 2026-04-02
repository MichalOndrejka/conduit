using Conduit.Rag.Sources;

namespace Conduit.Rag.Services;

public sealed class SyncService(
    ISourceConfigStore configStore,
    SourceFactory sourceFactory,
    IDocumentIndexer indexer) : ISyncService
{
    public async Task SyncAsync(string sourceId, CancellationToken ct = default)
    {
        var definition = await configStore.GetByIdAsync(sourceId, ct)
            ?? throw new InvalidOperationException($"Source '{sourceId}' not found.");

        definition.SyncStatus = "syncing";
        definition.SyncError  = null;
        await configStore.SaveAsync(definition, ct);

        try
        {
            var source    = sourceFactory.Create(definition);
            var documents = await source.FetchDocumentsAsync(ct);
            await indexer.IndexBatchAsync(source.CollectionName, documents, ct);

            definition.LastSyncedAt = DateTime.UtcNow;
            definition.SyncStatus   = "completed";
            definition.SyncError    = null;
            await configStore.SaveAsync(definition, ct);
        }
        catch (Exception ex)
        {
            definition.SyncStatus = "failed";
            definition.SyncError  = ex.Message;
            await configStore.SaveAsync(definition, ct);
        }
    }

    public async Task SyncAllAsync(CancellationToken ct = default)
    {
        var sources = await configStore.GetAllAsync(ct);
        foreach (var source in sources)
            await SyncAsync(source.Id, ct);
    }
}
