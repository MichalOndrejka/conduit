namespace Conduit.Rag.Ado;

/// <summary>
/// Resolved connection settings for a single ADO API context.
/// Supports PAT, Bearer, NTLM, Negotiate, API-key, and anonymous auth over HTTP/HTTPS.
/// </summary>
public sealed record AdoConnectionConfig
{
    /// <summary>
    /// Project-level base URL with no trailing slash.
    /// Examples:
    ///   Cloud:      https://dev.azure.com/myorg/MyProject
    ///   On-premise: https://ado.company.com/DefaultCollection/MyProject
    /// </summary>
    public required string BaseUrl { get; init; }

    /// <summary>Authentication type: pat | bearer | ntlm | negotiate | apikey | none</summary>
    public required string AuthType { get; init; }

    // Auth credentials — only the fields relevant to the chosen AuthType are used.
    public string? Pat { get; init; }          // pat
    public string? Token { get; init; }        // bearer
    public string? ApiKeyHeader { get; init; } // apikey — header name, e.g. "X-Api-Key"
    public string? ApiKeyValue { get; init; }  // apikey — header value
    public string? Username { get; init; }     // ntlm / negotiate (leave blank to use process identity)
    public string? Password { get; init; }     // ntlm / negotiate
    public string? Domain { get; init; }       // ntlm / negotiate

    /// <summary>
    /// REST API version string. Defaults to 7.1 (Azure DevOps Services / Server 2022).
    /// Set to 4.1 for TFS 2018, 5.1 for ADO Server 2019, 6.0 for ADO Server 2020.
    /// </summary>
    public string ApiVersion { get; init; } = "7.1";

    /// <summary>
    /// Builds an <see cref="AdoConnectionConfig"/> from a source config dictionary.
    /// Supports both new-style (<c>baseUrl</c> + <c>authType</c>) and legacy (<c>organization</c>
    /// + <c>project</c> + <c>pat</c>) configurations so existing sources keep working unchanged.
    /// </summary>
    public static AdoConnectionConfig From(IReadOnlyDictionary<string, string> config)
    {
        // ── Resolve base URL ────────────────────────────────────────────────
        string baseUrl;
        if (config.TryGetValue("baseUrl", out var raw) && !string.IsNullOrWhiteSpace(raw))
        {
            baseUrl = raw.TrimEnd('/');
        }
        else if (config.TryGetValue("organization", out var org) &&
                 config.TryGetValue("project",      out var project))
        {
            baseUrl = $"https://dev.azure.com/{org}/{project}";
        }
        else
        {
            throw new InvalidOperationException(
                "ADO source must specify 'baseUrl', or both 'organization' and 'project'.");
        }

        // ── Resolve auth type ───────────────────────────────────────────────
        // Explicit authType wins; fall back to "pat" if a pat key exists; else "none".
        var authType = config.TryGetValue("authType", out var at) && !string.IsNullOrWhiteSpace(at)
            ? at.Trim().ToLowerInvariant()
            : (config.ContainsKey("pat") ? "pat" : "none");

        var apiVersion = config.TryGetValue("apiVersion", out var av) && !string.IsNullOrWhiteSpace(av)
            ? av.Trim()
            : "7.1";

        return new AdoConnectionConfig
        {
            BaseUrl      = baseUrl,
            AuthType     = authType,
            ApiVersion   = apiVersion,
            Pat          = Resolve(config.GetValueOrDefault("pat")),
            Token        = Resolve(config.GetValueOrDefault("token")),
            ApiKeyHeader = config.GetValueOrDefault("apiKeyHeader"),
            ApiKeyValue  = Resolve(config.GetValueOrDefault("apiKeyValue")),
            Username     = config.GetValueOrDefault("username"),
            Password     = Resolve(config.GetValueOrDefault("password")),
            Domain       = config.GetValueOrDefault("domain"),
        };
    }

    /// <summary>Cache key used to share Windows-auth <see cref="HttpClient"/> instances.</summary>
    internal string WindowsAuthCacheKey =>
        $"{AuthType}|{Username}|{Domain}|{BaseUrl}";

    /// <summary>Treats the stored value as an environment variable name and resolves it at runtime.</summary>
    private static string? Resolve(string? envVarName) =>
        string.IsNullOrWhiteSpace(envVarName) ? null : Environment.GetEnvironmentVariable(envVarName);
}
