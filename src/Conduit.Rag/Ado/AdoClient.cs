using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text;
using System.Text.Json;

namespace Conduit.Rag.Ado;

/// <summary>
/// Thin HTTP client for the Azure DevOps REST API authenticated via PAT.
/// All methods accept the org/project/pat so that each source can target a different ADO instance.
/// </summary>
public sealed class AdoClient(HttpClient http) : IAdoClient
{
    private static readonly JsonSerializerOptions JsonOpts = new() { PropertyNameCaseInsensitive = true };

    // ── Work Items ──────────────────────────────────────────────────────────

    /// <summary>Run a WIQL query and return the requested fields for every matching work item.</summary>
    public async Task<IReadOnlyList<JsonElement>> RunWorkItemQueryAsync(
        string organization, string project, string pat,
        string wiql, IReadOnlyList<string> fields,
        CancellationToken ct = default)
    {
        var request = new HttpRequestMessage(
            HttpMethod.Post,
            $"https://dev.azure.com/{organization}/{project}/_apis/wit/wiql?api-version=7.1")
        {
            Content = JsonContent.Create(new { query = wiql }),
            Headers = { Authorization = MakeAuthHeader(pat) }
        };

        var wiqlResponse = await http.SendAsync(request, ct);
        wiqlResponse.EnsureSuccessStatusCode();

        var wiqlDoc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await wiqlResponse.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        var ids = wiqlDoc
            .GetProperty("workItems")
            .EnumerateArray()
            .Select(wi => wi.GetProperty("id").GetInt32())
            .ToList();

        if (ids.Count == 0) return [];

        // Batch fetch work item details (max 200 per call)
        var results = new List<JsonElement>();
        foreach (var batch in ids.Chunk(200))
        {
            var ids_csv = string.Join(",", batch);
            var fields_csv = string.Join(",", fields);
            var detailRequest = new HttpRequestMessage(
                HttpMethod.Get,
                $"https://dev.azure.com/{organization}/{project}/_apis/wit/workitems?ids={ids_csv}&fields={fields_csv}&api-version=7.1")
            {
                Headers = { Authorization = MakeAuthHeader(pat) }
            };

            var detailResponse = await http.SendAsync(detailRequest, ct);
            detailResponse.EnsureSuccessStatusCode();

            var detailDoc = await JsonSerializer.DeserializeAsync<JsonElement>(
                await detailResponse.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

            results.AddRange(detailDoc.GetProperty("value").EnumerateArray());
        }

        return results;
    }

    // ── Git / Code ──────────────────────────────────────────────────────────

    /// <summary>List all file paths under a scope path in a Git repository.</summary>
    public async Task<IReadOnlyList<string>> GetFileTreeAsync(
        string organization, string project, string pat,
        string repository, string branch, string scopePath = "/",
        CancellationToken ct = default)
    {
        var url = $"https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repository}/items" +
                  $"?scopePath={Uri.EscapeDataString(scopePath)}&recursionLevel=Full" +
                  $"&versionDescriptor.version={Uri.EscapeDataString(branch)}&api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url)
        {
            Headers = { Authorization = MakeAuthHeader(pat) }
        };

        var response = await http.SendAsync(request, ct);
        response.EnsureSuccessStatusCode();

        var doc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await response.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        return doc.GetProperty("value")
                  .EnumerateArray()
                  .Where(item => item.GetProperty("gitObjectType").GetString() == "blob")
                  .Select(item => item.GetProperty("path").GetString() ?? string.Empty)
                  .Where(p => !string.IsNullOrEmpty(p))
                  .ToList();
    }

    /// <summary>Get the raw text content of a single file from a Git repository.</summary>
    public async Task<string> GetFileContentAsync(
        string organization, string project, string pat,
        string repository, string branch, string path,
        CancellationToken ct = default)
    {
        var url = $"https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repository}/items" +
                  $"?path={Uri.EscapeDataString(path)}&versionDescriptor.version={Uri.EscapeDataString(branch)}&api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url)
        {
            Headers =
            {
                Authorization = MakeAuthHeader(pat),
                Accept = { new MediaTypeWithQualityHeaderValue("text/plain") }
            }
        };

        var response = await http.SendAsync(request, ct);
        response.EnsureSuccessStatusCode();

        return await response.Content.ReadAsStringAsync(ct);
    }

    // ── Pipelines / Builds ──────────────────────────────────────────────────

    /// <summary>Get the last N builds for a pipeline definition.</summary>
    public async Task<IReadOnlyList<JsonElement>> GetBuildsAsync(
        string organization, string project, string pat,
        string pipelineId, int lastN,
        CancellationToken ct = default)
    {
        var url = $"https://dev.azure.com/{organization}/{project}/_apis/build/builds" +
                  $"?definitions={pipelineId}&$top={lastN}&api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url)
        {
            Headers = { Authorization = MakeAuthHeader(pat) }
        };

        var response = await http.SendAsync(request, ct);
        response.EnsureSuccessStatusCode();

        var doc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await response.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        return doc.GetProperty("value").EnumerateArray().ToList();
    }

    /// <summary>Get the timeline (task records) for a build — used to extract failed task details.</summary>
    public async Task<IReadOnlyList<JsonElement>> GetBuildTimelineAsync(
        string organization, string project, string pat,
        string buildId,
        CancellationToken ct = default)
    {
        var url = $"https://dev.azure.com/{organization}/{project}/_apis/build/builds/{buildId}/timeline?api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url)
        {
            Headers = { Authorization = MakeAuthHeader(pat) }
        };

        var response = await http.SendAsync(request, ct);
        if (!response.IsSuccessStatusCode) return [];

        var doc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await response.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        if (!doc.TryGetProperty("records", out var records)) return [];
        return records.EnumerateArray().ToList();
    }

    // ── Helpers ─────────────────────────────────────────────────────────────

    private static AuthenticationHeaderValue MakeAuthHeader(string pat)
    {
        var token = Convert.ToBase64String(Encoding.ASCII.GetBytes($":{pat}"));
        return new AuthenticationHeaderValue("Basic", token);
    }
}
