using Conduit.Rag.Models;
using Qdrant.Client.Grpc;

namespace Conduit.Rag.Extensions;

public static class QdrantPayloadExtensions
{
    /// <summary>Convert a Qdrant gRPC payload to a SearchResult, reconstructing tags and properties from prefixed keys.</summary>
    public static SearchResult ToSearchResult(this ScoredPoint point)
    {
        var payload = point.Payload;

        var text       = payload.TryGetValue(PayloadKeys.Text, out var t) ? t.StringValue : string.Empty;
        var tags       = new Dictionary<string, string>();
        var properties = new Dictionary<string, string>();

        foreach (var (key, value) in payload)
        {
            if (key.StartsWith(PayloadKeys.TagPrefix))
                tags[key[PayloadKeys.TagPrefix.Length..]] = value.StringValue;
            else if (key.StartsWith(PayloadKeys.PropPrefix))
                properties[key[PayloadKeys.PropPrefix.Length..]] = value.StringValue;
        }

        return new SearchResult(
            Id:         point.Id?.Uuid ?? point.Id?.Num.ToString() ?? string.Empty,
            Score:      point.Score,
            Text:       text,
            Tags:       tags,
            Properties: properties
        );
    }

    public static IReadOnlyList<SearchResult> ToSearchResults(this IReadOnlyList<ScoredPoint> points)
        => points.Select(p => p.ToSearchResult()).ToList();
}
