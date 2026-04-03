using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Qdrant.Client.Grpc;
using static Qdrant.Client.Grpc.Conditions;

namespace Conduit.McpServer.Pages.Sources;

public class ItemsModel(ISourceConfigStore store, IVectorStore vectorStore) : PageModel
{
    private const int PageSize = 20;

    public SourceDefinition Source { get; private set; } = null!;

    public IReadOnlyList<EmbeddedItem> Items { get; private set; } = [];

    /// <summary>UUID string of the next page's starting point; null on the last page.</summary>
    public string? NextOffset { get; private set; }

    public async Task<IActionResult> OnGetAsync(string id, string? offset = null)
    {
        var source = await store.GetByIdAsync(id);
        if (source is null) return NotFound();

        var collection = CollectionNames.For(source.Type);
        if (collection is null) return NotFound();

        Source = source;

        var filter = new Filter();
        filter.Must.Add(MatchKeyword($"{PayloadKeys.TagPrefix}source_name", source.Name));

        PointId? qdrantOffset = offset is not null
            ? new PointId { Uuid = offset }
            : null;

        var (points, next) = await vectorStore.ScrollAsync(
            collection, filter, limit: PageSize, offset: qdrantOffset);

        Items = points.Select(p => new EmbeddedItem(
            Title:      GetPayload(p, $"{PayloadKeys.PropPrefix}title"),
            Url:        GetPayload(p, $"{PayloadKeys.PropPrefix}url")
                     ?? GetPayload(p, $"{PayloadKeys.PropPrefix}path"),
            Text:       GetPayload(p, PayloadKeys.Text) ?? string.Empty,
            ChunkIndex: GetPayload(p, PayloadKeys.ChunkIndex),
            TotalChunks: GetPayload(p, PayloadKeys.TotalChunks),
            IndexedAt:  GetPayload(p, PayloadKeys.IndexedAtMs) is { } ms
                            && long.TryParse(ms, out var msLong)
                            ? DateTimeOffset.FromUnixTimeMilliseconds(msLong).LocalDateTime
                            : null
        )).ToList();

        NextOffset = next?.Uuid;

        return Page();
    }

    private static string? GetPayload(RetrievedPoint point, string key) =>
        point.Payload.TryGetValue(key, out var v) ? v.StringValue : null;
}

public record EmbeddedItem(
    string? Title,
    string? Url,
    string Text,
    string? ChunkIndex,
    string? TotalChunks,
    DateTime? IndexedAt);
