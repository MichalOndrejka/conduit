using Conduit.Rag.Models;
using Grpc.Core;
using Microsoft.Extensions.Hosting;
using Qdrant.Client;
using Qdrant.Client.Grpc;
using System.Text.Json;

namespace Conduit.Rag.Services;

public sealed class QdrantBootstrapper(
    QdrantClient qdrant,
    int embeddingDim,
    string embeddingModel,
    string fingerprintPath,
    QdrantHealthStatus health) : BackgroundService
{
    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        for (var attempt = 1; attempt <= 30 && !stoppingToken.IsCancellationRequested; attempt++)
        {
            try
            {
                await DropCollectionsIfEmbeddingChangedAsync(stoppingToken);

                foreach (var collection in CollectionNames.All)
                    await EnsureCollectionAsync(collection, stoppingToken);

                WriteFingerprint();
                Console.WriteLine("[bootstrap] All Conduit collections are ready.");
                health.IsReady = true;
                health.Error   = null;
                return;
            }
            catch (Exception ex) when (!stoppingToken.IsCancellationRequested)
            {
                Console.WriteLine($"[bootstrap] attempt {attempt} failed: {ex.Message}");
                health.Error = ex.Message;
                await Task.Delay(TimeSpan.FromSeconds(1), stoppingToken);
            }
        }

        health.IsReady = false;
    }

    private async Task DropCollectionsIfEmbeddingChangedAsync(CancellationToken ct)
    {
        var (storedModel, storedDim) = ReadFingerprint();
        if (storedModel == embeddingModel && storedDim == embeddingDim)
            return;

        if (storedModel is not null || storedDim is not null)
        {
            Console.WriteLine(
                $"[bootstrap] Embedding changed ({storedModel}/{storedDim}d → {embeddingModel}/{embeddingDim}d). " +
                "Dropping all collections — sources will need to be re-indexed.");

            var existing = await qdrant.ListCollectionsAsync(ct);
            foreach (var name in CollectionNames.All.Where(n => existing.Contains(n)))
            {
                await qdrant.DeleteCollectionAsync(name, cancellationToken: ct);
                Console.WriteLine($"[bootstrap] Dropped collection '{name}'.");
            }
        }
    }

    private async Task EnsureCollectionAsync(string collection, CancellationToken ct)
    {
        try
        {
            await qdrant.CreateCollectionAsync(
                collectionName: collection,
                vectorsConfig: new VectorParams
                {
                    Size     = (uint)embeddingDim,
                    Distance = Distance.Cosine
                },
                cancellationToken: ct);

            Console.WriteLine($"[bootstrap] Created collection '{collection}'.");
        }
        catch (RpcException ex) when (ex.StatusCode == StatusCode.AlreadyExists)
        {
            // Already exists with the correct dimensions — nothing to do.
        }
    }

    private (string? Model, int? Dim) ReadFingerprint()
    {
        if (!File.Exists(fingerprintPath)) return (null, null);
        try
        {
            using var f   = File.OpenRead(fingerprintPath);
            var doc = JsonDocument.Parse(f);
            var model = doc.RootElement.TryGetProperty("model", out var m) ? m.GetString() : null;
            var dim   = doc.RootElement.TryGetProperty("dimensions", out var d) && d.TryGetInt32(out var i) ? i : (int?)null;
            return (model, dim);
        }
        catch
        {
            return (null, null);
        }
    }

    private void WriteFingerprint()
    {
        var json = JsonSerializer.Serialize(new { model = embeddingModel, dimensions = embeddingDim },
            new JsonSerializerOptions { WriteIndented = true });
        File.WriteAllText(fingerprintPath, json);
    }
}
