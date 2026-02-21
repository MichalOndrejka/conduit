using Qdrant.Client.Grpc;
using static Qdrant.Client.Grpc.Conditions;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Production implementation of <see cref="IQdrantFilterFactory"/>.
/// Each tag becomes a condition on the <c>tag_{key}</c> payload field.
/// </summary>
public sealed class QdrantFilterFactory : IQdrantFilterFactory
{
    /// <inheritdoc />
    public Filter? CreateGrpcFilter(Dictionary<string, string>? tags)
    {
        if (tags is null || tags.Count == 0) return null;

        var filter = new Filter();

        foreach (var (key, value) in tags)
        {
            filter.Must.Add(MatchKeyword($"tag_{key}", value));
        }

        return filter;
    }

    /// <inheritdoc />
    public object? CreateScrollFilter(Dictionary<string, string>? tags)
    {
        if (tags is null || tags.Count == 0) return null;

        var must = new List<object>();

        foreach (var (key, value) in tags)
        {
            must.Add(new { key = $"tag_{key}", match = new { value } });
        }

        return new { must };
    }
}
