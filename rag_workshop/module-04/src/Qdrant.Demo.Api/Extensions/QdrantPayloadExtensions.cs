using Qdrant.Client.Grpc;
using Qdrant.Demo.Api.Models;

namespace Qdrant.Demo.Api.Extensions;

/// <summary>
/// Extension methods for Qdrant gRPC payload types.
/// </summary>
/// <remarks>
/// ⚠️  Qdrant.Client uses <see cref="Qdrant.Client.Grpc.Value"/> —
/// NOT <c>Google.Protobuf.WellKnownTypes.Value</c>.
/// Use <c>DoubleValue</c> / <c>IntegerValue</c> (there is no <c>NumberValue</c>).
/// </remarks>
public static class QdrantPayloadExtensions
{
    /// <summary>Convert a Qdrant gRPC payload dictionary to plain CLR objects for clean JSON output.</summary>
    public static Dictionary<string, object?> ToDictionary(
        this IDictionary<string, Value> payload)
    {
        return payload.ToDictionary(kv => kv.Key, kv => FromProto(kv.Value));
    }

    /// <summary>
    /// Format a list of <see cref="ScoredPoint"/> as typed <see cref="SearchHit"/> records.
    /// </summary>
    public static IEnumerable<SearchHit> ToFormattedHits(
        this IReadOnlyList<ScoredPoint> hits)
    {
        return hits.Select(h => new SearchHit(
            Id: h.Id?.Uuid ?? h.Id?.Num.ToString(),
            Score: h.Score,
            Payload: h.Payload.ToDictionary()
        ));
    }

    // ── private ─────────────────────────────────────────────

    private static object? FromProto(Value v) =>
        v.KindCase switch
        {
            Value.KindOneofCase.StringValue  => v.StringValue,
            Value.KindOneofCase.DoubleValue  => v.DoubleValue,
            Value.KindOneofCase.IntegerValue => v.IntegerValue,
            Value.KindOneofCase.BoolValue    => v.BoolValue,
            Value.KindOneofCase.StructValue  =>
                v.StructValue.Fields.ToDictionary(f => f.Key, f => FromProto(f.Value)),
            Value.KindOneofCase.ListValue    =>
                v.ListValue.Values.Select(FromProto).ToList(),
            _ => null
        };
}
