using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace Conduit.Rag.Sources;

/// <summary>Fetches ADO test cases and embeds title, steps, and expected results.</summary>
public sealed class AdoTestCaseSource(SourceDefinition definition, IAdoClient ado) : ISource
{
    public string Type           => SourceTypes.AdoTestCase;
    public string CollectionName => CollectionNames.AdoTestCases;

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(IProgress<string>? fetchProgress = null, CancellationToken ct = default)
    {
        var conn   = AdoConnectionConfig.From(definition.Config);
        var query  = definition.Config[ConfigKeys.Query];
        var fields = ParseFields(definition.Config.GetValueOrDefault(ConfigKeys.Fields, ""));
        var workItems = await ado.RunWorkItemQueryAsync(conn, query, fields, ct);
        return workItems.Select(ToDocument).ToList();
    }

    private SourceDocument ToDocument(JsonElement wi)
    {
        var f    = wi.GetProperty("fields");
        var id   = wi.GetProperty("id").GetInt32().ToString();

        var title            = GetString(f, "System.Title");
        var state            = GetString(f, "System.State");
        var stepsXml         = GetString(f, "Microsoft.VSTS.TCM.Steps");
        var automationStatus = GetString(f, "Microsoft.VSTS.Common.AutomationStatus");

        var text = new StringBuilder();
        text.AppendLine($"Title: {title}");
        text.AppendLine($"State: {state}");
        if (!string.IsNullOrWhiteSpace(stepsXml))
        {
            text.AppendLine("Steps:");
            text.AppendLine(ParseTestSteps(stepsXml));
        }

        return new SourceDocument(
            Id:   $"tc-{id}",
            Text: text.ToString().Trim(),
            Tags: new Dictionary<string, string>
            {
                ["source_name"]       = definition.Name,
                ["automation_status"] = automationStatus,
                ["state"]             = state
            },
            Properties: new Dictionary<string, string>
            {
                ["id"]    = id,
                ["title"] = title,
                ["url"]   = wi.TryGetProperty("url", out var u) ? u.GetString() ?? string.Empty : string.Empty
            }
        );
    }

    /// <summary>Strips HTML/XML tags from ADO's test step XML and returns plain text.</summary>
    private static string ParseTestSteps(string stepsXml)
    {
        var plain = Regex.Replace(stepsXml, "<[^>]+>", " ");
        return Regex.Replace(plain, @"\s+", " ").Trim();
    }

    private static string GetString(JsonElement fields, string key)
        => fields.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String
            ? v.GetString() ?? string.Empty
            : string.Empty;

    private static IReadOnlyList<string> ParseFields(string csv)
        => csv.Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
}
