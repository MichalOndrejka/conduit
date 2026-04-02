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

        var source = sourceFactory.Create(definition);

        IReadOnlyList<Models.SourceDocument> documents;
        try
        {
            documents = await source.FetchDocumentsAsync(ct);
        }
        catch (Exception ex)
        {
            definition.SyncStatus = "failed";
            definition.SyncError  = $"[Fetch error] {ex.Message}";
            await configStore.SaveAsync(definition, ct);
            return;
        }

        try
        {
            await indexer.IndexBatchAsync(source.CollectionName, documents, ct);
            definition.LastSyncedAt = DateTime.UtcNow;
            definition.SyncStatus   = "completed";
            definition.SyncError    = null;
        }
        catch (Exception ex)
        {
            definition.SyncStatus = "failed";
            definition.SyncError  = $"[Embedding error] {ex.Message}";
        }

        await configStore.SaveAsync(definition, ct);
    }

    public async Task SyncAllAsync(CancellationToken ct = default)
    {
        var sources = await configStore.GetAllAsync(ct);
        foreach (var source in sources)
            await SyncAsync(source.Id, ct);
    }
}
