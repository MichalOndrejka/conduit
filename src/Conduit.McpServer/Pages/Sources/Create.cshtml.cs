using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using System.Text;
using UglyToad.PdfPig;

namespace Conduit.McpServer.Pages.Sources;

public class CreateModel(ISourceConfigStore store, ISyncService syncService) : PageModel
{
    [BindProperty]
    public SourceDefinition Source { get; set; } = new();

    public string? SelectedType { get; set; }
    public string TypeLabel { get; set; } = string.Empty;

    public IReadOnlyList<(string Type, string Label, string Description)> SourceTypes { get; } =
    [
        (Conduit.Rag.Models.SourceTypes.ManualDocument,   "Document",           "Paste text or upload a file (txt, md, pdf) — architecture docs, ADRs, design notes."),
        (Conduit.Rag.Models.SourceTypes.AdoWorkItemQuery, "ADO Work Items",     "Index work items matching a WIQL query (bugs, user stories, tasks)."),
        (Conduit.Rag.Models.SourceTypes.AdoCodeRepo,      "ADO Code Repo",      "Index source files from an Azure DevOps Git repository."),
        (Conduit.Rag.Models.SourceTypes.AdoPipelineBuild, "ADO Pipeline Builds","Index recent build results and failed task logs from a pipeline."),
        (Conduit.Rag.Models.SourceTypes.AdoRequirements,  "ADO Requirements",   "Index product requirements, software requirements, and risk mitigations."),
        (Conduit.Rag.Models.SourceTypes.AdoTestCase,      "ADO Test Cases",     "Index test cases with steps and expected results."),
        (Conduit.Rag.Models.SourceTypes.AdoWiki,          "ADO Wiki",           "Index Azure DevOps wiki pages as searchable markdown sections."),
        (Conduit.Rag.Models.SourceTypes.HttpPage,         "HTTP Page",          "Fetch and index content from any HTTP/HTTPS URL.")
    ];

    public void OnGet(string? type)
    {
        SelectedType = type;
        Source.Type  = type ?? string.Empty;
        TypeLabel    = SourceTypes.FirstOrDefault(t => t.Type == type).Label ?? type ?? string.Empty;
    }

    public async Task<IActionResult> OnPostAsync(
        string? configContent,
        IFormFile? configFile,
        string? configBaseUrl, string? configAuthType,
        string? configPat, string? configToken,
        string? configApiKeyHeader, string? configApiKeyValue,
        string? configUsername, string? configPassword, string? configDomain,
        string? configQuery, string? configFields,
        string? configRepository, string? configBranch, string? configGlobPatterns,
        string? configPipelineId, string? configLastNBuilds,
        string? configWikiName, string? configPathFilter,
        string? configUrl, string? configTitle, string? configContentType)
    {
        SelectedType = Source.Type;
        TypeLabel    = SourceTypes.FirstOrDefault(t => t.Type == Source.Type).Label ?? Source.Type;

        if (string.IsNullOrWhiteSpace(Source.Name))
            ModelState.AddModelError("Source.Name", "Name is required.");

        if (configFile is not null)
            configContent = await ReadFileAsync(configFile);

        BuildConfig(configContent,
                    configBaseUrl, configAuthType,
                    configPat, configToken,
                    configApiKeyHeader, configApiKeyValue,
                    configUsername, configPassword, configDomain,
                    configQuery, configFields,
                    configRepository, configBranch, configGlobPatterns,
                    configPipelineId, configLastNBuilds,
                    configWikiName, configPathFilter,
                    configUrl, configTitle, configContentType);

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
        string? baseUrl, string? authType,
        string? pat, string? token,
        string? apiKeyHeader, string? apiKeyValue,
        string? username, string? password, string? domain,
        string? query, string? fields,
        string? repository, string? branch, string? globPatterns,
        string? pipelineId, string? lastNBuilds,
        string? wikiName, string? pathFilter,
        string? url, string? title, string? contentType)
    {
        void Set(string key, string? value)
        {
            if (!string.IsNullOrWhiteSpace(value))
                Source.Config[key] = value.Trim();
        }

        Set(ConfigKeys.Content, content);

        // Connection
        if (!string.IsNullOrWhiteSpace(baseUrl))
        {
            Source.Config[ConfigKeys.BaseUrl] = baseUrl.Trim().TrimEnd('/');
            Source.Config.Remove(ConfigKeys.Organization);
            Source.Config.Remove(ConfigKeys.Project);
        }

        var resolvedAuthType = string.IsNullOrWhiteSpace(authType) ? "none" : authType.Trim().ToLowerInvariant();
        Source.Config[ConfigKeys.AuthType] = resolvedAuthType;

        // Clear all credential keys, then set only the ones relevant to the chosen auth type
        foreach (var k in new[] { ConfigKeys.Pat, ConfigKeys.Token, ConfigKeys.ApiKeyHeader, ConfigKeys.ApiKeyValue,
                                  ConfigKeys.Username, ConfigKeys.Password, ConfigKeys.Domain })
            Source.Config.Remove(k);

        switch (resolvedAuthType)
        {
            case "pat":      Set(ConfigKeys.Pat, pat); break;
            case "bearer":   Set(ConfigKeys.Token, token); break;
            case "ntlm":
            case "negotiate":
                Set(ConfigKeys.Username, username);
                Set(ConfigKeys.Password, password);
                Set(ConfigKeys.Domain,   domain);
                break;
            case "apikey":
                Set(ConfigKeys.ApiKeyHeader, apiKeyHeader);
                Set(ConfigKeys.ApiKeyValue,  apiKeyValue);
                break;
        }

        // Source-specific
        Set(ConfigKeys.Query,        query);
        Set(ConfigKeys.Fields,       fields);
        Set(ConfigKeys.Repository,   repository);
        Set(ConfigKeys.Branch,       branch);
        Set(ConfigKeys.GlobPatterns, globPatterns);
        Set(ConfigKeys.PipelineId,   pipelineId);
        Set(ConfigKeys.LastNBuilds,  lastNBuilds);
        Set(ConfigKeys.WikiName,     wikiName);
        Set(ConfigKeys.PathFilter,   pathFilter);
        Set(ConfigKeys.Url,          url);
        Set(ConfigKeys.Title,        title);
        Set(ConfigKeys.ContentType,  contentType);
    }

    private static async Task<string> ReadFileAsync(IFormFile file)
    {
        var ext = Path.GetExtension(file.FileName).ToLowerInvariant();
        await using var stream = file.OpenReadStream();

        if (ext == ".pdf")
        {
            using var doc = PdfDocument.Open(stream);
            var sb = new StringBuilder();
            foreach (var page in doc.GetPages())
                sb.AppendLine(page.Text);
            return sb.ToString();
        }

        using var reader = new StreamReader(stream, Encoding.UTF8);
        return await reader.ReadToEndAsync();
    }
}
