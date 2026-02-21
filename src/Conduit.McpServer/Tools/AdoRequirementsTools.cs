using System.ComponentModel;
using System.Text.Json;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using ModelContextProtocol.Server;

namespace Conduit.McpServer.Tools;

[McpServerToolType]
public static class AdoRequirementsTools
{
    [McpServerTool(Name = "search_ado_requirements")]
    [Description("Search product requirements, software requirements, and risk mitigations from Azure DevOps. Returns requirement documents matching the query ranked by semantic similarity.")]
    public static async Task<string> Search(
        ISearchService searchService,
        [Description("The search query")] string query,
        [Description("Maximum number of results to return (default 5)")] int topK = 5,
        [Description("Filter to a specific requirements source by its configured name")] string? sourceName = null,
        CancellationToken ct = default)
    {
        var tags = sourceName is not null
            ? new Dictionary<string, string> { ["source_name"] = sourceName }
            : null;

        var results = await searchService.SearchAsync(CollectionNames.AdoRequirements, query, topK, tags, ct);
        return JsonSerializer.Serialize(results);
    }
}
