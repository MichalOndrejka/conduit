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
            IQdrantFilterFactory filters,
            CancellationToken ct) =>
        {
            try
            {
                var vector = await embeddings.EmbedAsync(req.QueryText, ct);
                var filter = filters.CreateGrpcFilter(req.Tags);

                var hits = await qdrant.SearchAsync(
                    collectionName: collectionName,
                    vector: vector,
                    limit: (ulong)req.K,
                    filter: filter,
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

        // ─────────────────────────────────────────────────────
        // POST /search/threshold — all results above score
        // ─────────────────────────────────────────────────────
        app.MapPost("/search/threshold", async (
            [FromBody] ThresholdSearchRequest req,
            QdrantClient qdrant,
            IEmbeddingService embeddings,
            IQdrantFilterFactory filters,
            CancellationToken ct) =>
        {
            try
            {
                var vector = await embeddings.EmbedAsync(req.QueryText, ct);
                var filter = filters.CreateGrpcFilter(req.Tags);

                var hits = await qdrant.SearchAsync(
                    collectionName: collectionName,
                    vector: vector,
                    limit: (ulong)req.Limit,
                    filter: filter,
                    scoreThreshold: req.ScoreThreshold,
                    payloadSelector: true,
                    cancellationToken: ct);

                return Results.Ok(hits.ToFormattedHits());
            }
            catch (Exception ex)
            {
                Console.WriteLine($"[search/threshold] Error: {ex.Message}");
                return Results.Problem(
                    detail: ex.Message, statusCode: 500, title: "Threshold search failed");
            }
        });

        // ─────────────────────────────────────────────────────
        // POST /search/metadata — no vector, tag filters only
        // ─────────────────────────────────────────────────────
        app.MapPost("/search/metadata", async (
            [FromBody] MetadataSearchRequest req,
            QdrantClient qdrant,
            IQdrantFilterFactory filters,
            CancellationToken ct) =>
        {
            try
            {
                var filter = filters.CreateGrpcFilter(req.Tags);

                var scroll = await qdrant.ScrollAsync(
                    collectionName: collectionName,
                    filter: filter,
                    limit: (uint)req.Limit,
                    payloadSelector: true,
                    cancellationToken: ct);

                var results = scroll.Result.Select(p => new SearchHit(
                    Id: p.Id?.Uuid ?? p.Id?.Num.ToString(),
                    Score: 0f,
                    Payload: p.Payload.ToDictionary()
                ));

                return Results.Ok(results);
            }
            catch (Exception ex)
            {
                Console.WriteLine($"[search/metadata] Error: {ex.Message}");
                return Results.Problem(
                    detail: ex.Message, statusCode: 500, title: "Metadata search failed");
            }
        });

        return app;
    }
}
