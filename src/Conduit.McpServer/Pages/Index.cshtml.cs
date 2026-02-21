using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;

namespace Conduit.McpServer.Pages;

public class IndexModel(ISourceConfigStore store) : PageModel
{
    public IReadOnlyList<SourceDefinition> Sources { get; private set; } = [];

    public async Task OnGetAsync()
    {
        Sources = await store.GetAllAsync();
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
