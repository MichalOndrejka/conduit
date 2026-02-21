using System.Text.Json;

namespace Conduit.Rag.Ado;

public interface IAdoClient
{
    Task<IReadOnlyList<JsonElement>> RunWorkItemQueryAsync(
        string organization, string project, string pat,
        string wiql, IReadOnlyList<string> fields,
        CancellationToken ct = default);

    Task<IReadOnlyList<string>> GetFileTreeAsync(
        string organization, string project, string pat,
        string repository, string branch, string scopePath = "/",
        CancellationToken ct = default);

    Task<string> GetFileContentAsync(
        string organization, string project, string pat,
        string repository, string branch, string path,
        CancellationToken ct = default);

    Task<IReadOnlyList<JsonElement>> GetBuildsAsync(
        string organization, string project, string pat,
        string pipelineId, int lastN,
        CancellationToken ct = default);

    Task<IReadOnlyList<JsonElement>> GetBuildTimelineAsync(
        string organization, string project, string pat,
        string buildId,
        CancellationToken ct = default);
}
