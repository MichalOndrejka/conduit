using System.Collections.Concurrent;
using System.Net;
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text;
using System.Text.Json;

namespace Conduit.Rag.Ado;

/// <summary>
/// HTTP client for the Azure DevOps REST API.
/// <para>
/// Supported auth types (set via <c>authType</c> in source config):
/// <list type="bullet">
///   <item><term>pat</term><description>HTTP Basic with Personal Access Token (default when a <c>pat</c> key is present)</description></item>
///   <item><term>bearer</term><description>Bearer / OAuth token (<c>token</c> key)</description></item>
///   <item><term>ntlm</term><description>NTLM Windows authentication — on-premise AD; uses process identity when <c>username</c> is omitted</description></item>
///   <item><term>negotiate</term><description>Kerberos/Negotiate — on-premise; uses process identity when <c>username</c> is omitted</description></item>
///   <item><term>apikey</term><description>Custom header (<c>apiKeyHeader</c> + <c>apiKeyValue</c>)</description></item>
///   <item><term>none</term><description>Anonymous / no authentication</description></item>
/// </list>
/// </para>
/// <para>
/// Works with both Azure DevOps Services (<c>dev.azure.com</c>) and on-premise deployments on
/// any custom domain. The project-level base URL is supplied via <see cref="AdoConnectionConfig"/>.
/// </para>
/// </summary>
public sealed class AdoClient(HttpClient http) : IAdoClient, IDisposable
{
    private static readonly JsonSerializerOptions JsonOpts = new() { PropertyNameCaseInsensitive = true };

    // Windows-auth (NTLM/Negotiate) HttpClient instances are cached to reuse connections,
    // avoiding socket exhaustion when a source fetches many files.
    private readonly ConcurrentDictionary<string, HttpClient> _windowsClients = new();

    // ── Work Items ──────────────────────────────────────────────────────────

    /// <summary>Run a WIQL query and return the requested fields for every matching work item.</summary>
    public async Task<IReadOnlyList<JsonElement>> RunWorkItemQueryAsync(
        AdoConnectionConfig conn,
        string wiql, IReadOnlyList<string> fields,
        CancellationToken ct = default)
    {
        var client = GetClient(conn);

        var wiqlRequest = new HttpRequestMessage(
            HttpMethod.Post,
            $"{conn.BaseUrl}/_apis/wit/wiql?api-version=7.1")
        {
            Content = JsonContent.Create(new { query = wiql })
        };
        ApplyAuth(wiqlRequest, conn);

        var wiqlResponse = await client.SendAsync(wiqlRequest, ct);
        wiqlResponse.EnsureSuccessStatusCode();

        var wiqlDoc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await wiqlResponse.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        var ids = wiqlDoc
            .GetProperty("workItems")
            .EnumerateArray()
            .Select(wi => wi.GetProperty("id").GetInt32())
            .ToList();

        if (ids.Count == 0) return [];

        // Batch-fetch work item details (max 200 per call)
        var results = new List<JsonElement>();
        foreach (var batch in ids.Chunk(200))
        {
            var idsCsv    = string.Join(",", batch);
            var fieldsCsv = string.Join(",", fields);
            var detailRequest = new HttpRequestMessage(
                HttpMethod.Get,
                $"{conn.BaseUrl}/_apis/wit/workitems?ids={idsCsv}&fields={fieldsCsv}&api-version=7.1");
            ApplyAuth(detailRequest, conn);

            var detailResponse = await client.SendAsync(detailRequest, ct);
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
        AdoConnectionConfig conn,
        string repository, string branch, string scopePath = "/",
        CancellationToken ct = default)
    {
        var url = $"{conn.BaseUrl}/_apis/git/repositories/{repository}/items" +
                  $"?scopePath={Uri.EscapeDataString(scopePath)}&recursionLevel=Full" +
                  $"&versionDescriptor.version={Uri.EscapeDataString(branch)}&api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url);
        ApplyAuth(request, conn);

        var response = await GetClient(conn).SendAsync(request, ct);
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
        AdoConnectionConfig conn,
        string repository, string branch, string path,
        CancellationToken ct = default)
    {
        var url = $"{conn.BaseUrl}/_apis/git/repositories/{repository}/items" +
                  $"?path={Uri.EscapeDataString(path)}&versionDescriptor.version={Uri.EscapeDataString(branch)}&api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url);
        ApplyAuth(request, conn);
        request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("text/plain"));

        var response = await GetClient(conn).SendAsync(request, ct);
        response.EnsureSuccessStatusCode();

        return await response.Content.ReadAsStringAsync(ct);
    }

    // ── Pipelines / Builds ──────────────────────────────────────────────────

    /// <summary>Get the last N builds for a pipeline definition.</summary>
    public async Task<IReadOnlyList<JsonElement>> GetBuildsAsync(
        AdoConnectionConfig conn,
        string pipelineId, int lastN,
        CancellationToken ct = default)
    {
        var url = $"{conn.BaseUrl}/_apis/build/builds" +
                  $"?definitions={pipelineId}&$top={lastN}&api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url);
        ApplyAuth(request, conn);

        var response = await GetClient(conn).SendAsync(request, ct);
        response.EnsureSuccessStatusCode();

        var doc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await response.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        return doc.GetProperty("value").EnumerateArray().ToList();
    }

    /// <summary>Get the timeline (task records) for a build — used to extract failed task details.</summary>
    public async Task<IReadOnlyList<JsonElement>> GetBuildTimelineAsync(
        AdoConnectionConfig conn,
        string buildId,
        CancellationToken ct = default)
    {
        var url = $"{conn.BaseUrl}/_apis/build/builds/{buildId}/timeline?api-version=7.1";

        var request = new HttpRequestMessage(HttpMethod.Get, url);
        ApplyAuth(request, conn);

        var response = await GetClient(conn).SendAsync(request, ct);
        if (!response.IsSuccessStatusCode) return [];

        var doc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await response.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        if (!doc.TryGetProperty("records", out var records)) return [];
        return records.EnumerateArray().ToList();
    }

    // ── Wiki ────────────────────────────────────────────────────────────────────

    /// <summary>List all wikis in the ADO project.</summary>
    public async Task<IReadOnlyList<JsonElement>> GetWikisAsync(
        AdoConnectionConfig conn,
        CancellationToken ct = default)
    {
        var url     = $"{conn.BaseUrl}/_apis/wiki/wikis?api-version=7.1";
        var request = new HttpRequestMessage(HttpMethod.Get, url);
        ApplyAuth(request, conn);

        var response = await GetClient(conn).SendAsync(request, ct);
        response.EnsureSuccessStatusCode();

        var doc = await JsonSerializer.DeserializeAsync<JsonElement>(
            await response.Content.ReadAsStreamAsync(ct), JsonOpts, ct);

        return doc.GetProperty("value").EnumerateArray().ToList();
    }

    // ── Helpers ─────────────────────────────────────────────────────────────

    /// <summary>
    /// Returns the right <see cref="HttpClient"/> for the connection:
    /// a shared client for header-based auth, a cached dedicated client for Windows auth.
    /// </summary>
    private HttpClient GetClient(AdoConnectionConfig conn)
    {
        if (conn.AuthType is "ntlm" or "negotiate")
            return _windowsClients.GetOrAdd(conn.WindowsAuthCacheKey, _ => CreateWindowsClient(conn));

        return http;
    }

    private static HttpClient CreateWindowsClient(AdoConnectionConfig conn)
    {
        var handler = new SocketsHttpHandler
        {
            PooledConnectionLifetime = TimeSpan.FromMinutes(15)
        };

        handler.Credentials = !string.IsNullOrWhiteSpace(conn.Username)
            ? new NetworkCredential(conn.Username, conn.Password, conn.Domain)
            : CredentialCache.DefaultCredentials;

        return new HttpClient(handler);
    }

    /// <summary>Applies the configured auth strategy to a request message.</summary>
    private static void ApplyAuth(HttpRequestMessage request, AdoConnectionConfig conn)
    {
        switch (conn.AuthType)
        {
            case "pat" when !string.IsNullOrEmpty(conn.Pat):
                var encoded = Convert.ToBase64String(Encoding.ASCII.GetBytes($":{conn.Pat}"));
                request.Headers.Authorization = new AuthenticationHeaderValue("Basic", encoded);
                break;

            case "bearer" when !string.IsNullOrEmpty(conn.Token):
                request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", conn.Token);
                break;

            case "apikey" when !string.IsNullOrEmpty(conn.ApiKeyHeader) && !string.IsNullOrEmpty(conn.ApiKeyValue):
                request.Headers.TryAddWithoutValidation(conn.ApiKeyHeader, conn.ApiKeyValue);
                break;

            // ntlm / negotiate: the SocketsHttpHandler performs the handshake automatically.
            // none: no header needed.
        }
    }

    public void Dispose()
    {
        foreach (var client in _windowsClients.Values)
            client.Dispose();
    }
}
