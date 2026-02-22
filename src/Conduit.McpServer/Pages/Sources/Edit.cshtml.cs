using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;

namespace Conduit.McpServer.Pages.Sources;

public class EditModel(ISourceConfigStore store, ISyncService syncService) : PageModel
{
    [BindProperty]
    public SourceDefinition Source { get; set; } = new();

    public string TypeLabel { get; set; } = string.Empty;

    private static readonly Dictionary<string, string> TypeLabels = new()
    {
        [SourceTypes.ManualDocument]   = "Manual Document",
        [SourceTypes.AdoWorkItemQuery] = "ADO Work Items",
        [SourceTypes.AdoCodeRepo]      = "ADO Code Repo",
        [SourceTypes.AdoPipelineBuild] = "ADO Pipeline Builds",
        [SourceTypes.AdoRequirements]  = "ADO Requirements",
        [SourceTypes.AdoTestCase]      = "ADO Test Cases"
    };

    public async Task<IActionResult> OnGetAsync(string id)
    {
        var source = await store.GetByIdAsync(id);
        if (source is null) return NotFound();

        Source    = source;
        TypeLabel = TypeLabels.GetValueOrDefault(source.Type, source.Type);
        return Page();
    }

    public async Task<IActionResult> OnPostAsync(
        string? configContent,
        string? configOrganization, string? configProject, string? configPat,
        string? configQuery, string? configFields,
        string? configRepository, string? configBranch, string? configGlobPatterns,
        string? configPipelineId, string? configLastNBuilds)
    {
        TypeLabel = TypeLabels.GetValueOrDefault(Source.Type, Source.Type);

        if (string.IsNullOrWhiteSpace(Source.Name))
            ModelState.AddModelError("Source.Name", "Name is required.");

        BuildConfig(configContent, configOrganization, configProject, configPat,
                    configQuery, configFields, configRepository, configBranch, configGlobPatterns,
                    configPipelineId, configLastNBuilds);

        if (!ModelState.IsValid)
            return Page();

        Source.SyncStatus = "syncing";
        await store.SaveAsync(Source);
        _ = Task.Run(() => syncService.SyncAsync(Source.Id, CancellationToken.None));
        return RedirectToPage("/Index");
    }

    public string GetConfig(string key, string defaultValue = "") =>
        Source.Config.GetValueOrDefault(key, defaultValue);

    private void BuildConfig(
        string? content,
        string? org, string? project, string? pat,
        string? query, string? fields,
        string? repository, string? branch, string? globPatterns,
        string? pipelineId, string? lastNBuilds)
    {
        void Set(string key, string? value)
        {
            if (!string.IsNullOrWhiteSpace(value))
                Source.Config[key] = value.Trim();
        }

        Set(ConfigKeys.Content,      content);
        Set(ConfigKeys.Organization, org);
        Set(ConfigKeys.Project,      project);
        Set(ConfigKeys.Pat,          pat);
        Set(ConfigKeys.Query,        query);
        Set(ConfigKeys.Fields,       fields);
        Set(ConfigKeys.Repository,   repository);
        Set(ConfigKeys.Branch,       branch);
        Set(ConfigKeys.GlobPatterns, globPatterns);
        Set(ConfigKeys.PipelineId,   pipelineId);
        Set(ConfigKeys.LastNBuilds,  lastNBuilds);
    }
}
