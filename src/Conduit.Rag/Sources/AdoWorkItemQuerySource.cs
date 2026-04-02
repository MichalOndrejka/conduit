using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using System.Text;
using System.Text.Json;

namespace Conduit.Rag.Sources;

/// <summary>Fetches work items matching a WIQL query and embeds their fields as documents.</summary>
public sealed class AdoWorkItemQuerySource(SourceDefinition definition, IAdoClient ado) : ISource
{
    public string Type           => SourceTypes.AdoWorkItemQuery;
    public string CollectionName => CollectionNames.AdoWorkItems;

    // Broad field set — null fields are skipped at embed time, so each WIT naturally
    // contributes only the fields that exist on it (bugs get repro steps, stories get
    // acceptance criteria, etc.)
    private static readonly IReadOnlyList<string> Fields =
    [
        "System.Id",
        "System.Title",
        "System.Description",
        "System.WorkItemType",
        "System.State",
        "System.AreaPath",
        "System.Tags",
        "Microsoft.VSTS.Common.AcceptanceCriteria",
        "Microsoft.VSTS.Common.ReproSteps",
        "Microsoft.VSTS.Common.SystemInfo",
        "Microsoft.VSTS.TCM.Steps",
        "Microsoft.VSTS.Common.Priority",
        "Microsoft.VSTS.Scheduling.StoryPoints",
    ];

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(CancellationToken ct = default)
    {
        var conn      = AdoConnectionConfig.From(definition.Config);
        var query     = definition.Config[ConfigKeys.Query];
        var workItems = await ado.RunWorkItemQueryAsync(conn, query, Fields, ct);
        return workItems.Select(ToDocument).ToList();
    }

    private SourceDocument ToDocument(JsonElement wi)
    {
        var f  = wi.GetProperty("fields");
        var id = wi.GetProperty("id").GetInt32().ToString();

        var text = new StringBuilder();
        foreach (var field in Fields)
        {
            if (f.TryGetProperty(field, out var val) && val.ValueKind != JsonValueKind.Null)
            {
                var label = field.Split('.').Last();
                text.AppendLine($"{label}: {val}");
            }
        }

        return new SourceDocument(
            Id:   $"wi-{id}",
            Text: text.ToString().Trim(),
            Tags: new Dictionary<string, string>
            {
                ["source_name"]    = definition.Name,
                ["work_item_type"] = GetString(f, "System.WorkItemType"),
                ["state"]          = GetString(f, "System.State")
            },
            Properties: new Dictionary<string, string>
            {
                ["id"]    = id,
                ["title"] = GetString(f, "System.Title"),
                ["url"]   = wi.TryGetProperty("url", out var u) ? u.GetString() ?? string.Empty : string.Empty
            }
        );
    }

    private static string GetString(JsonElement fields, string key)
        => fields.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String
            ? v.GetString() ?? string.Empty
            : string.Empty;
}
