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

        // ─── Batch document upsert ───────────────────────────
        app.MapPost("/documents/batch", async (
            [FromBody] IReadOnlyList<DocumentUpsertRequest> batch,
            IDocumentIndexer indexer,
            CancellationToken ct) =>
        {
            List<string> errors = [];
            var succeeded = 0;

            foreach (var req in batch)
            {
                if (string.IsNullOrWhiteSpace(req.Text))
                {
                    var label = req.Id ?? "(empty)";
                    errors.Add($"[{label}]: Text is required and cannot be empty.");
                    continue;
                }

                try
                {
                    await indexer.IndexAsync(req, ct);
                    succeeded++;
                }
                catch (Exception ex)
                {
                    var label = req.Id ?? req.Text[..Math.Min(req.Text.Length, 40)];
                    errors.Add($"[{label}]: {ex.Message}");
                }
            }

            return Results.Ok(new BatchUpsertResponse(
                Total: batch.Count,
                Succeeded: succeeded,
                Failed: errors.Count,
                Errors: errors));
        });

        return app;
    }
}
