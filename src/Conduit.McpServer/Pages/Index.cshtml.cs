using System.Text;
using System.Text.Json;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;

namespace Conduit.McpServer.Pages;

public class IndexModel(ISourceConfigStore store, ISyncService syncService, SyncProgressStore progressStore) : PageModel
{
    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        WriteIndented          = true,
        PropertyNameCaseInsensitive = true
    };

    private static readonly HashSet<string> SecretKeys = new(StringComparer.OrdinalIgnoreCase)
    {
        ConfigKeys.Pat, ConfigKeys.Token, ConfigKeys.ApiKeyValue, ConfigKeys.Password
    };

    public IReadOnlyList<SourceDefinition> Sources { get; private set; } = [];

    [TempData] public string? ImportMessage { get; set; }
    [TempData] public string? ImportError   { get; set; }

    public async Task OnGetAsync()
    {
        Sources = await store.GetAllAsync();
    }

    public async Task<IActionResult> OnPostSyncAsync(string id)
    {
        var source = await store.GetByIdAsync(id);
        if (source is null) return NotFound();

        _ = Task.Run(() => syncService.SyncAsync(id, CancellationToken.None));
        return RedirectToPage();
    }

    public async Task<JsonResult> OnGetStatusAsync()
    {
        var sources = await store.GetAllAsync();
        return new JsonResult(sources.Select(s =>
        {
            var p = progressStore.Get(s.Id);
            return new
            {
                id           = s.Id,
                syncStatus   = s.SyncStatus,
                lastSyncedAt = s.LastSyncedAt?.ToString("yyyy-MM-dd HH:mm"),
                syncError    = s.SyncError,
                syncPhase    = p?.Phase,
                syncCurrent  = p?.Current,
                syncTotal    = p?.Total,
                syncMessage  = p?.Message
            };
        }));
    }

    public async Task<IActionResult> OnGetExportAsync()
    {
        var sources = await store.GetAllAsync();
        var export  = sources.Select(s => new
        {
            s.Id,
            s.Type,
            s.Name,
            Config = StripSecrets(s.Config)
        });

        var json  = JsonSerializer.Serialize(export, JsonOptions);
        var bytes = Encoding.UTF8.GetBytes(json);
        return File(bytes, "application/json", "conduit-sources.json");
    }

    public async Task<IActionResult> OnPostImportAsync(IFormFile? file)
    {
        if (file is null || file.Length == 0)
        {
            ImportError = "No file selected.";
            return RedirectToPage();
        }

        List<SourceDefinition>? imported;
        try
        {
            await using var stream = file.OpenReadStream();
            imported = await JsonSerializer.DeserializeAsync<List<SourceDefinition>>(stream, JsonOptions);
        }
        catch
        {
            ImportError = "Invalid JSON — could not parse the file.";
            return RedirectToPage();
        }

        if (imported is null || imported.Count == 0)
        {
            ImportError = "No sources found in the file.";
            return RedirectToPage();
        }

        var existingIds = (await store.GetAllAsync()).Select(s => s.Id).ToHashSet();

        foreach (var source in imported)
        {
            if (existingIds.Contains(source.Id))
                source.Id = Guid.NewGuid().ToString("D");

            source.SyncStatus   = "idle";
            source.SyncError    = null;
            source.LastSyncedAt = null;

            await store.SaveAsync(source);
        }

        ImportMessage = $"{imported.Count} source(s) imported. Fill in any credentials and sync each source.";
        return RedirectToPage();
    }

    private static Dictionary<string, string> StripSecrets(Dictionary<string, string> config)
    {
        var result = new Dictionary<string, string>(config);
        foreach (var key in SecretKeys)
            if (result.ContainsKey(key))
                result[key] = "";
        return result;
    }
}
