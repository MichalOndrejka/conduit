using Qdrant.Client.Grpc;
using static Qdrant.Client.Grpc.Conditions;

namespace Conduit.Rag.Services;

public sealed class QdrantFilterFactory : IQdrantFilterFactory
{
    public Filter? CreateGrpcFilter(Dictionary<string, string>? tags)
    {
        if (tags is null || tags.Count == 0) return null;

        var filter = new Filter();
        foreach (var (key, value) in tags)
            filter.Must.Add(MatchKeyword($"tag_{key}", value));

        return filter;
    }
}
