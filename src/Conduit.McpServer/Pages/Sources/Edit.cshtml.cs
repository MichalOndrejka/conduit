using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using System.Text;
using UglyToad.PdfPig;

namespace Conduit.McpServer.Pages.Sources;

public class EditModel(ISourceConfigStore store, ISyncService syncService) : PageModel
{
    [BindProperty]
    public SourceDefinition Source { get; set; } = new();

    public string TypeLabel { get; set; } = string.Empty;

    private static readonly Dictionary<string, string> TypeLabels = new()
    {
        [SourceTypes.ManualDocument]   = "Document",
        [SourceTypes.AdoWorkItemQuery] = "ADO Work Items",
        [SourceTypes.AdoCodeRepo]      = "ADO Code Repo",
        [SourceTypes.AdoPipelineBuild] = "ADO Pipeline Builds",
        [SourceTypes.AdoRequirements]  = "ADO Requirements",
        [SourceTypes.AdoTestCase]      = "ADO Test Cases",
        [SourceTypes.AdoWiki]          = "ADO Wiki",
        [SourceTypes.HttpPage]         = "HTTP Page"
    };

    public async Task<IActionResult> OnGetAsync(string id)
    {
        var source = await store.GetByIdAsync(id);
        if (source is null) return NotFound();

        Source    = source;
        TypeLabel = TypeLabels.GetValueOrDefault(source.Type, source.Type);

        // Migrate legacy org+project to baseUrl for display so the field is pre-populated
        if (!Source.Config.ContainsKey(ConfigKeys.BaseUrl) &&
            Source.Config.TryGetValue(ConfigKeys.Organization, out var org) &&
            Source.Config.TryGetValue(ConfigKeys.Project, out var proj))
        {
            Source.Config[ConfigKeys.BaseUrl] = $"https://dev.azure.com/{org}/{proj}";
        }

        return Page();
    }

    public async Task<IActionResult> OnPostAsync(
        string? configContent,
        IFormFile? configFile,
        string? configBaseUrl, string? configAuthType, string? configApiVersion,
        string? configPat, string? configToken,
        string? configApiKeyHeader, string? configApiKeyValue,
        string? configUsername, string? configPassword, string? configDomain,
        string? configQuery,
        string? configRepository, string? configBranch, string? configGlobPatterns,
        string? configPipelineId, string? configLastNBuilds,
        string? configWikiName, string? configPathFilter,
        string? configUrl, string? configTitle, string? configContentType)
    {
        TypeLabel = TypeLabels.GetValueOrDefault(Source.Type, Source.Type);

        if (string.IsNullOrWhiteSpace(Source.Name))
            ModelState.AddModelError("Source.Name", "Name is required.");

        if (configFile is not null)
            configContent = await ReadFileAsync(configFile);

        BuildConfig(configContent,
                    configBaseUrl, configAuthType, configApiVersion,
                    configPat, configToken,
                    configApiKeyHeader, configApiKeyValue,
                    configUsername, configPassword, configDomain,
                    configQuery,
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
        string? baseUrl, string? authType, string? apiVersion,
        string? pat, string? token,
        string? apiKeyHeader, string? apiKeyValue,
        string? username, string? password, string? domain,
        string? query,
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
        Set(ConfigKeys.ApiVersion, apiVersion);

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
