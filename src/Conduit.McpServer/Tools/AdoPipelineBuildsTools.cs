using System.ComponentModel;
using System.Text.Json;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using ModelContextProtocol.Server;

namespace Conduit.McpServer.Tools;

[McpServerToolType]
public static class AdoPipelineBuildsTools
{
    [McpServerTool(Name = "search_ado_builds")]
    [Description("Search Azure DevOps pipeline build results, failures, and failed task logs. Useful for finding recurring failures, understanding what broke in a build, or analysing test failures.")]
    public static async Task<string> Search(
        ISearchService searchService,
        [Description("The search query")] string query,
        [Description("Maximum number of results to return (default 5)")] int topK = 5,
        [Description("Filter to a specific pipeline source by its configured name")] string? sourceName = null,
        CancellationToken ct = default)
    {
        var tags = sourceName is not null
            ? new Dictionary<string, string> { ["source_name"] = sourceName }
            : null;

        var results = await searchService.SearchAsync(CollectionNames.AdoBuilds, query, topK, tags, ct);
        return JsonSerializer.Serialize(results);
    }
}
