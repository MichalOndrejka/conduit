using Microsoft.AspNetCore.Mvc;
using Qdrant.Client;
using Qdrant.Demo.Api.Extensions;
using Qdrant.Demo.Api.Models;
using Qdrant.Demo.Api.Services;

namespace Qdrant.Demo.Api.Endpoints;

public static class SearchEndpoints
{
    public static WebApplication MapSearchEndpoints(this WebApplication app, string collectionName)
    {
        // ─────────────────────────────────────────────────────
        // POST /search/topk — fixed result count
        // ─────────────────────────────────────────────────────
        app.MapPost("/search/topk", async (
            [FromBody] TopKSearchRequest req,
            QdrantClient qdrant,
            IEmbeddingService embeddings,
            CancellationToken ct) =>
        {
            try
            {
                var vector = await embeddings.EmbedAsync(req.QueryText, ct);

                var hits = await qdrant.SearchAsync(
                    collectionName: collectionName,
                    vector: vector,
                    limit: (ulong)req.K,
                    payloadSelector: true,
                    cancellationToken: ct);

                return Results.Ok(hits.ToFormattedHits());
            }
            catch (Exception ex)
            {
                Console.WriteLine($"[search/topk] Error: {ex.Message}");
                return Results.Problem(
                    detail: ex.Message, statusCode: 500, title: "Top-K search failed");
            }
        });

        return app;
    }
}
