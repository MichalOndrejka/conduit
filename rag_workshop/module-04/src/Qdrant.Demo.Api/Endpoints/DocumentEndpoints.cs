using Microsoft.AspNetCore.Mvc;
using Qdrant.Demo.Api.Models;
using Qdrant.Demo.Api.Services;

namespace Qdrant.Demo.Api.Endpoints;

public static class DocumentEndpoints
{
    public static WebApplication MapDocumentEndpoints(this WebApplication app)
    {
        // ─── Single document upsert ──────────────────────────
        app.MapPost("/documents", async (
            [FromBody] DocumentUpsertRequest req,
            IDocumentIndexer indexer,
            CancellationToken ct) =>
        {
            if (string.IsNullOrWhiteSpace(req.Text))
                return Results.BadRequest("Text is required and cannot be empty.");

            try
            {
                var response = await indexer.IndexAsync(req, ct);
                return Results.Ok(response);
            }
            catch (Exception ex)
            {
                Console.WriteLine($"[documents] Error: {ex.Message}");
                return Results.Problem(
                    detail: ex.Message, statusCode: 500, title: "Indexing failed");
            }
        });

        return app;
    }
}
