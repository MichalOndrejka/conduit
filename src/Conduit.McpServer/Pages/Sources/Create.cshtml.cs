using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;

namespace Conduit.McpServer.Pages.Sources;

public class CreateModel(ISourceConfigStore store, ISyncService syncService) : PageModel
{
    [BindProperty]
    public SourceDefinition Source { get; set; } = new();

    public string? SelectedType { get; set; }
    public string TypeLabel { get; set; } = string.Empty;

    public IReadOnlyList<(string Type, string Label, string Description)> SourceTypes { get; } =
    [
        (Conduit.Rag.Models.SourceTypes.ManualDocument,   "Manual Document",    "Paste or type a document directly — architecture docs, ADRs, design notes."),
        (Conduit.Rag.Models.SourceTypes.AdoWorkItemQuery, "ADO Work Items",     "Index work items matching a WIQL query (bugs, user stories, tasks)."),
        (Conduit.Rag.Models.SourceTypes.AdoCodeRepo,      "ADO Code Repo",      "Index source files from an Azure DevOps Git repository."),
        (Conduit.Rag.Models.SourceTypes.AdoPipelineBuild, "ADO Pipeline Builds","Index recent build results and failed task logs from a pipeline."),
        (Conduit.Rag.Models.SourceTypes.AdoRequirements,  "ADO Requirements",   "Index product requirements, software requirements, and risk mitigations."),
        (Conduit.Rag.Models.SourceTypes.AdoTestCase,      "ADO Test Cases",     "Index test cases with steps and expected results.")
    ];

    public void OnGet(string? type)
    {
        SelectedType = type;
        Source.Type  = type ?? string.Empty;
        TypeLabel    = SourceTypes.FirstOrDefault(t => t.Type == type).Label ?? type ?? string.Empty;
    }

    public async Task<IActionResult> OnPostAsync(
        string? configTitle, string? configContent,
        string? configOrganization, string? configProject, string? configPat,
        string? configQuery, string? configFields,
        string? configRepository, string? configBranch, string? configGlobPatterns,
        string? configPipelineId, string? configLastNBuilds)
    {
        SelectedType = Source.Type;
        TypeLabel    = SourceTypes.FirstOrDefault(t => t.Type == Source.Type).Label ?? Source.Type;

        if (string.IsNullOrWhiteSpace(Source.Name))
            ModelState.AddModelError("Source.Name", "Name is required.");

        BuildConfig(configTitle, configContent, configOrganization, configProject, configPat,
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
        string? title, string? content,
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

        Set(ConfigKeys.Title,        title);
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
