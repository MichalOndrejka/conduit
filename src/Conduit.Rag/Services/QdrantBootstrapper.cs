using Conduit.Rag.Models;
using Grpc.Core;
using Microsoft.Extensions.Hosting;
using Qdrant.Client;
using Qdrant.Client.Grpc;

namespace Conduit.Rag.Services;

public sealed class QdrantBootstrapper(QdrantClient qdrant, int embeddingDim) : BackgroundService
{
    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        for (var attempt = 1; attempt <= 30 && !stoppingToken.IsCancellationRequested; attempt++)
        {
            try
            {
                foreach (var collection in CollectionNames.All)
                    await EnsureCollectionAsync(collection, stoppingToken);

                Console.WriteLine("[bootstrap] All Conduit collections are ready.");
                return;
            }
            catch (Exception ex) when (!stoppingToken.IsCancellationRequested)
            {
                Console.WriteLine($"[bootstrap] attempt {attempt} failed: {ex.Message}");
                await Task.Delay(TimeSpan.FromSeconds(1), stoppingToken);
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
            // Already exists from a previous run — nothing to do.
        }
    }
}
