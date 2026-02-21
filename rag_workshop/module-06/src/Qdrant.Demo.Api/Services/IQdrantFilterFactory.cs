using Qdrant.Client.Grpc;

namespace Qdrant.Demo.Api.Services;

/// <summary>
/// Builds Qdrant filter objects from tag dictionaries.
/// Two flavours are provided: gRPC (<see cref="Filter"/>) for the
/// managed client, and an anonymous object for the REST scroll API.
/// </summary>
public interface IQdrantFilterFactory
{
    /// <summary>
    /// Build a gRPC <see cref="Filter"/> with a <c>MatchKeyword</c> condition
    /// per tag entry.  Returns <c>null</c> when <paramref name="tags"/> is null or empty.
    /// </summary>
    Filter? CreateGrpcFilter(Dictionary<string, string>? tags);

    /// <summary>
    /// Build a REST-compatible anonymous filter object for the Qdrant scroll endpoint.
    /// Returns <c>null</c> when <paramref name="tags"/> is null or empty.
    /// </summary>
    object? CreateScrollFilter(Dictionary<string, string>? tags);
}
