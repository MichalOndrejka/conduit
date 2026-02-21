namespace Conduit.Rag.Models;

public static class SourceTypes
{
    public const string ManualDocument   = "manual";
    public const string AdoWorkItemQuery = "ado-workitem-query";
    public const string AdoCodeRepo      = "ado-code";
    public const string AdoPipelineBuild = "ado-pipeline-build";
    public const string AdoRequirements  = "ado-requirements";
    public const string AdoTestCase      = "ado-test-case";

    public static readonly IReadOnlyList<string> All =
    [
        ManualDocument, AdoWorkItemQuery, AdoCodeRepo,
        AdoPipelineBuild, AdoRequirements, AdoTestCase
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

    public static readonly IReadOnlyList<string> All =
    [
        ManualDocuments, AdoWorkItems, AdoCode,
        AdoBuilds, AdoRequirements, AdoTestCases
    ];
}

/// <summary>Well-known config dictionary keys per source type.</summary>
public static class ConfigKeys
{
    // Shared across all ADO sources
    public const string Organization = "organization";
    public const string Project      = "project";
    public const string Pat          = "pat";

    // Manual document
    public const string Title   = "title";
    public const string Content = "content";

    // ADO query-based (work items, requirements, test cases)
    public const string Query  = "query";
    public const string Fields = "fields"; // comma-separated field names

    // ADO code repo
    public const string Repository    = "repository";
    public const string Branch        = "branch";
    public const string GlobPatterns  = "globPatterns"; // comma-separated, e.g. "**/*.cs,**/*.md"

    // ADO pipeline build
    public const string PipelineId  = "pipelineId";
    public const string LastNBuilds = "lastNBuilds";
}
