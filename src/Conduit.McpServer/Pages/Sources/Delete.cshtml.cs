using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;

namespace Conduit.McpServer.Pages.Sources;

public class DeleteModel(ISourceConfigStore store) : PageModel
{
    public SourceDefinition Source { get; set; } = new();

    public async Task<IActionResult> OnGetAsync(string id)
    {
        var source = await store.GetByIdAsync(id);
        if (source is null) return NotFound();

        Source = source;
        return Page();
    }

    public async Task<IActionResult> OnPostAsync(string id)
    {
        await store.DeleteAsync(id);
        return RedirectToPage("/Index");
    }
}
