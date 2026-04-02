using System.Text.Json;

namespace Conduit.Rag.Ado;

public interface IAdoClient
{
    Task<IReadOnlyList<JsonElement>> RunWorkItemQueryAsync(
        AdoConnectionConfig conn,
        string wiql, IReadOnlyList<string> fields,
        CancellationToken ct = default);

    Task<IReadOnlyList<string>> GetFileTreeAsync(
        AdoConnectionConfig conn,
        string repository, string branch, string scopePath = "/",
        CancellationToken ct = default);

    Task<string> GetFileContentAsync(
        AdoConnectionConfig conn,
        string repository, string branch, string path,
        CancellationToken ct = default);

    Task<IReadOnlyList<JsonElement>> GetBuildsAsync(
        AdoConnectionConfig conn,
        string pipelineId, int lastN,
        CancellationToken ct = default);

    Task<IReadOnlyList<JsonElement>> GetBuildTimelineAsync(
        AdoConnectionConfig conn,
        string buildId,
        CancellationToken ct = default);

    Task<IReadOnlyList<JsonElement>> GetWikisAsync(
        AdoConnectionConfig conn,
        CancellationToken ct = default);
}
