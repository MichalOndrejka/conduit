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

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(CancellationToken ct = default)
    {
        var conn   = AdoConnectionConfig.From(definition.Config);
        var query  = definition.Config[ConfigKeys.Query];
        var fields = ParseFields(definition.Config.GetValueOrDefault(ConfigKeys.Fields, ""));
        var workItems = await ado.RunWorkItemQueryAsync(conn, query, fields, ct);
        return workItems.Select(wi => ToDocument(wi, fields)).ToList();
    }

    private SourceDocument ToDocument(JsonElement wi, IReadOnlyList<string> requestedFields)
    {
        var f  = wi.GetProperty("fields");
        var id = wi.GetProperty("id").GetInt32().ToString();

        // If specific fields were requested use those; otherwise embed all string/number fields
        var fieldsToEmbed = requestedFields.Count > 0
            ? requestedFields
            : f.EnumerateObject().Select(p => p.Name).ToList();

        var text = new StringBuilder();
        foreach (var field in fieldsToEmbed)
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

    private static IReadOnlyList<string> ParseFields(string csv)
        => csv.Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
}
