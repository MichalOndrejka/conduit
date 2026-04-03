namespace Conduit.Rag.Models;

public static class SourceTypes
{
    public const string ManualDocument   = "manual";
    public const string AdoWorkItemQuery = "ado-workitem-query";
    public const string AdoCodeRepo      = "ado-code";
    public const string AdoPipelineBuild = "ado-pipeline-build";
    public const string AdoRequirements  = "ado-requirements";
    public const string AdoTestCase      = "ado-test-case";
    public const string AdoWiki          = "ado-wiki";
    public const string HttpPage         = "http-page";

    public static readonly IReadOnlyList<string> All =
    [
        ManualDocument, AdoWorkItemQuery, AdoCodeRepo,
        AdoPipelineBuild, AdoRequirements, AdoTestCase,
        AdoWiki, HttpPage
    ];
}

public static class CollectionNames
{
    public const string ManualDocuments = "conduit_manual_documents";
    public const string AdoWorkItems    = "conduit_ado_workitems";
    public const string AdoCode         = "conduit_ado_code";
    public const string AdoBuilds       = "conduit_ado_builds";
    public const string AdoRequirements = "conduit_ado_requirements";
    public const string AdoTestCases    = "conduit_ado_testcases";
    public const string AdoWiki         = "conduit_ado_wiki";
    public const string HttpPages       = "conduit_http_pages";

    public static readonly IReadOnlyList<string> All =
    [
        ManualDocuments, AdoWorkItems, AdoCode,
        AdoBuilds, AdoRequirements, AdoTestCases,
        AdoWiki, HttpPages
    ];

    public static string? For(string sourceType) => sourceType switch
    {
        SourceTypes.ManualDocument   => ManualDocuments,
        SourceTypes.AdoWorkItemQuery => AdoWorkItems,
        SourceTypes.AdoCodeRepo      => AdoCode,
        SourceTypes.AdoPipelineBuild => AdoBuilds,
        SourceTypes.AdoRequirements  => AdoRequirements,
        SourceTypes.AdoTestCase      => AdoTestCases,
        SourceTypes.AdoWiki          => AdoWiki,
        SourceTypes.HttpPage         => HttpPages,
        _                            => null
    };
}

/// <summary>Well-known config dictionary keys per source type.</summary>
public static class ConfigKeys
{
    // ── ADO connection (new-style) ──────────────────────────────────────────
    /// <summary>
    /// Project-level base URL. Takes precedence over <see cref="Organization"/> + <see cref="Project"/>.
    /// Examples:
    ///   Cloud:      https://dev.azure.com/myorg/MyProject
    ///   On-premise: https://ado.company.com/DefaultCollection/MyProject
    /// </summary>
    public const string BaseUrl = "baseUrl";

    /// <summary>
    /// Authentication type. One of: pat | bearer | ntlm | negotiate | apikey | none.
    /// Defaults to "pat" when a <see cref="Pat"/> key is present, otherwise "none".
    /// </summary>
    public const string AuthType   = "authType";
    public const string ApiVersion = "apiVersion"; // e.g. "7.1" (ADO Services), "4.1" (TFS 2018), "5.1" (ADO Server 2019)

    // ── Legacy ADO connection (still supported for backward compatibility) ──
    public const string Organization = "organization";
    public const string Project      = "project";

    // ── Auth credentials ────────────────────────────────────────────────────
    public const string Pat          = "pat";          // authType=pat
    public const string Token        = "token";        // authType=bearer
    public const string ApiKeyHeader = "apiKeyHeader"; // authType=apikey — header name, e.g. "X-Api-Key"
    public const string ApiKeyValue  = "apiKeyValue";  // authType=apikey — header value
    public const string Username     = "username";     // authType=ntlm|negotiate (optional; omit for process identity)
    public const string Password     = "password";     // authType=ntlm|negotiate
    public const string Domain       = "domain";       // authType=ntlm|negotiate

    // ── Manual document ─────────────────────────────────────────────────────
    public const string Title   = "title";
    public const string Content = "content";

    // ── ADO query-based (work items, requirements, test cases) ──────────────
    public const string Query  = "query";
    public const string Fields = "fields"; // comma-separated field names

    // ── ADO code repo ───────────────────────────────────────────────────────
    public const string Repository   = "repository";
    public const string Branch       = "branch";
    public const string GlobPatterns = "globPatterns"; // comma-separated, e.g. "**/*.cs,**/*.md"

    // ── ADO pipeline build ──────────────────────────────────────────────────
    public const string PipelineId  = "pipelineId";
    public const string LastNBuilds = "lastNBuilds";

    // ── ADO wiki ─────────────────────────────────────────────────────────────
    public const string WikiName   = "wikiName";   // optional; defaults to first wiki found
    public const string PathFilter = "pathFilter"; // optional path prefix, e.g. /Architecture

    // ── Generic HTTP page ─────────────────────────────────────────────────────
    public const string Url         = "url";
    public const string ContentType = "contentType"; // auto | html | json | text
}
