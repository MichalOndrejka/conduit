using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using System.Text;
using System.Text.Json;

namespace Conduit.Rag.Sources;

/// <summary>Fetches recent pipeline build results and failed task details for RAG indexing.</summary>
public sealed class AdoPipelineBuildSource(SourceDefinition definition, IAdoClient ado) : ISource
{
    public string Type           => SourceTypes.AdoPipelineBuild;
    public string CollectionName => CollectionNames.AdoBuilds;

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(CancellationToken ct = default)
    {
        var org        = definition.Config[ConfigKeys.Organization];
        var project    = definition.Config[ConfigKeys.Project];
        var pat        = definition.Config[ConfigKeys.Pat];
        var pipelineId = definition.Config[ConfigKeys.PipelineId];
        var lastN      = int.TryParse(definition.Config.GetValueOrDefault(ConfigKeys.LastNBuilds, "5"), out var n) ? n : 5;

        var builds = await ado.GetBuildsAsync(org, project, pat, pipelineId, lastN, ct);

        var documents = new List<SourceDocument>(builds.Count);

        foreach (var build in builds)
        {
            ct.ThrowIfCancellationRequested();

            var buildId     = build.GetProperty("id").GetInt32().ToString();
            var buildNumber = GetString(build, "buildNumber");
            var status      = GetString(build, "status");
            var result      = GetString(build, "result");
            var startTime   = GetString(build, "startTime");
            var finishTime  = GetString(build, "finishTime");
            var url         = build.TryGetProperty("_links", out var links)
                              && links.TryGetProperty("web", out var web)
                              && web.TryGetProperty("href", out var href)
                              ? href.GetString() ?? string.Empty : string.Empty;

            var text = new StringBuilder();
            text.AppendLine($"Build Number: {buildNumber}");
            text.AppendLine($"Pipeline ID: {pipelineId}");
            text.AppendLine($"Status: {status}");
            text.AppendLine($"Result: {result}");
            text.AppendLine($"Start: {startTime}");
            text.AppendLine($"Finish: {finishTime}");

            // Fetch timeline to get failed task details
            var timeline = await ado.GetBuildTimelineAsync(org, project, pat, buildId, ct);
            var failedRecords = timeline
                .Where(r => r.TryGetProperty("result", out var res) && res.GetString() == "failed")
                .ToList();

            if (failedRecords.Count > 0)
            {
                text.AppendLine("Failed Tasks:");
                foreach (var record in failedRecords)
                {
                    var taskName     = GetString(record, "name");
                    var errorMessage = record.TryGetProperty("issues", out var issues)
                        ? string.Join("; ", issues.EnumerateArray()
                            .Select(i => i.TryGetProperty("message", out var m) ? m.GetString() : null)
                            .Where(m => m is not null))
                        : string.Empty;

                    text.AppendLine($"  - {taskName}: {errorMessage}");
                }
            }

            documents.Add(new SourceDocument(
                Id:         $"build-{buildId}",
                Text:       text.ToString().Trim(),
                Tags: new Dictionary<string, string>
                {
                    ["source_name"]  = definition.Name,
                    ["pipeline_id"]  = pipelineId,
                    ["build_result"] = result,
                    ["status"]       = status
                },
                Properties: new Dictionary<string, string>
                {
                    ["build_id"]     = buildId,
                    ["build_number"] = buildNumber,
                    ["finish_time"]  = finishTime,
                    ["url"]          = url
                }
            ));
        }

        return documents;
    }

    private static string GetString(JsonElement element, string key)
        => element.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String
            ? v.GetString() ?? string.Empty
            : string.Empty;
}
