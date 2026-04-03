using Conduit.Rag.Models;
using Conduit.Rag.Sources;

namespace Conduit.Rag.Services;

public sealed class SyncService(
    ISourceConfigStore configStore,
    SourceFactory sourceFactory,
    IDocumentIndexer indexer,
    SyncProgressStore progressStore) : ISyncService
{
    public async Task SyncAsync(string sourceId, CancellationToken ct = default)
    {
        var definition = await configStore.GetByIdAsync(sourceId, ct)
            ?? throw new InvalidOperationException($"Source '{sourceId}' not found.");

        definition.SyncStatus = "syncing";
        definition.SyncError  = null;
        await configStore.SaveAsync(definition, ct);

        var source = sourceFactory.Create(definition);

        IReadOnlyList<SourceDocument> documents;
        try
        {
            progressStore.Set(sourceId, new SyncProgress("fetching", 0, 0));
            documents = await source.FetchDocumentsAsync(ct);
        }
        catch (Exception ex)
        {
            progressStore.Clear(sourceId);
            definition.SyncStatus = "failed";
            definition.SyncError  = $"[Fetch error] {ex.Message}";
            await configStore.SaveAsync(definition, ct);
            return;
        }

        try
        {
            progressStore.Set(sourceId, new SyncProgress("embedding", 0, documents.Count));

            var progress = new Progress<(int current, int total)>(p =>
                progressStore.Set(sourceId, new SyncProgress("embedding", p.current, p.total)));

            await indexer.IndexBatchAsync(source.CollectionName, documents, progress, ct);
            definition.LastSyncedAt = DateTime.UtcNow;
            definition.SyncStatus   = "completed";
            definition.SyncError    = null;
        }
        catch (Exception ex)
        {
            definition.SyncStatus = "failed";
            definition.SyncError  = $"[Embedding error] {ex.Message}";
        }

        progressStore.Clear(sourceId);
        await configStore.SaveAsync(definition, ct);
    }

    public async Task SyncAllAsync(CancellationToken ct = default)
    {
        var sources = await configStore.GetAllAsync(ct);
        foreach (var source in sources)
            await SyncAsync(source.Id, ct);
    }
}
