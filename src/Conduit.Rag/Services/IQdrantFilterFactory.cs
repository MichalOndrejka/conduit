using Qdrant.Client.Grpc;

namespace Conduit.Rag.Services;

public interface IQdrantFilterFactory
{
    Filter? CreateGrpcFilter(Dictionary<string, string>? tags);
}
