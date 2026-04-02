using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;

namespace Conduit.McpServer.Pages;

public class IndexModel(ISourceConfigStore store, ISyncService syncService) : PageModel
{
    public IReadOnlyList<SourceDefinition> Sources { get; private set; } = [];

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
        return new JsonResult(sources.Select(s => new
        {
            id           = s.Id,
            syncStatus   = s.SyncStatus,
            lastSyncedAt = s.LastSyncedAt?.ToString("yyyy-MM-dd HH:mm"),
            syncError    = s.SyncError
        }));
    }
}
