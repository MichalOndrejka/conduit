using Grpc.Core;
using Qdrant.Client;
using Qdrant.Client.Grpc;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Hosted service that ensures the Qdrant collection exists at startup.
/// Retries up to 30 times (1 s apart) to tolerate the case where the API
/// container starts before Qdrant is ready.
/// </summary>
sealed class QdrantBootstrapper(QdrantClient qdrant, string collection, int dim) : BackgroundService
{
    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        for (var attempt = 1; attempt <= 30 && !stoppingToken.IsCancellationRequested; attempt++)
        {
            try
            {
                await EnsureCollectionAsync(stoppingToken);
                Console.WriteLine($"[bootstrap] Collection '{collection}' ready.");
                return;
            }
            catch (Exception ex) when (!stoppingToken.IsCancellationRequested)
            {
                Console.WriteLine($"[bootstrap] attempt {attempt} failed: {ex.Message}");
                await Task.Delay(TimeSpan.FromSeconds(1), stoppingToken);
            }
        }
    }

    private async Task EnsureCollectionAsync(CancellationToken ct)
    {
        try
        {
            await qdrant.CreateCollectionAsync(
                collectionName: collection,
                vectorsConfig: new VectorParams
                {
                    Size = (uint)dim,
                    Distance = Distance.Cosine
                },
                cancellationToken: ct);
        }
        catch (RpcException ex) when (ex.StatusCode == StatusCode.AlreadyExists)
        {
            // ok — collection already created on a previous run
        }
    }
}
