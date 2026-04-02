using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using System.Text;
using System.Text.Json;

namespace Conduit.Rag.Sources;

/// <summary>
/// Fetches requirements work items (product reqs, software reqs, risk mitigations)
/// using a WIQL query and embeds them into the requirements collection.
/// Structurally the same as AdoWorkItemQuerySource but targets a separate collection
/// and emphasises requirement-specific fields.
/// </summary>
public sealed class AdoRequirementsSource(SourceDefinition definition, IAdoClient ado) : ISource
{
    public string Type           => SourceTypes.AdoRequirements;
    public string CollectionName => CollectionNames.AdoRequirements;

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(CancellationToken ct = default)
    {
        var conn   = AdoConnectionConfig.From(definition.Config);
        var query  = definition.Config[ConfigKeys.Query];
        var fields = ParseFields(definition.Config.GetValueOrDefault(ConfigKeys.Fields,
            "System.Id,System.Title,System.Description,System.WorkItemType,System.State,Microsoft.VSTS.Common.AcceptanceCriteria"));

        var workItems = await ado.RunWorkItemQueryAsync(conn, query, fields, ct);

        return workItems.Select(wi => ToDocument(wi, fields)).ToList();
    }

    private SourceDocument ToDocument(JsonElement wi, IReadOnlyList<string> fields)
    {
        var f  = wi.GetProperty("fields");
        var id = wi.GetProperty("id").GetInt32().ToString();

        var text = new StringBuilder();
        foreach (var field in fields)
        {
            if (f.TryGetProperty(field, out var val) && val.ValueKind != JsonValueKind.Null)
            {
                var label = field.Split('.').Last();
                text.AppendLine($"{label}: {val}");
            }
        }

        var wiType = GetString(f, "System.WorkItemType");
        var state  = GetString(f, "System.State");

        return new SourceDocument(
            Id:         $"req-{id}",
            Text:       text.ToString().Trim(),
            Tags: new Dictionary<string, string>
            {
                ["source_name"]      = definition.Name,
                ["requirement_type"] = wiType,
                ["state"]            = state
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

    private static IReadOnlyList<string> ParseFields(string csv)
        => csv.Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
}
