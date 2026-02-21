using Conduit.McpServer.Pages.Sources;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Moq;
using NUnit.Framework;

namespace Conduit.McpServer.Tests.Pages.Sources;

[TestFixture]
public class DeleteModelTests
{
    private Mock<ISourceConfigStore> _store = null!;

    [SetUp]
    public void SetUp() => _store = new Mock<ISourceConfigStore>();

    // ─── OnGetAsync ──────────────────────────────────────────────

    [Test]
    public async Task OnGetAsync_LoadsSourceFromStore()
    {
        var source = new SourceDefinition { Id = "del", Name = "Old Source", Type = SourceTypes.ManualDocument };
        _store.Setup(s => s.GetByIdAsync("del", It.IsAny<CancellationToken>()))
              .ReturnsAsync(source);

        var model  = new DeleteModel(_store.Object);
        var result = await model.OnGetAsync("del");

        Assert.That(result,           Is.InstanceOf<PageResult>());
        Assert.That(model.Source.Id,  Is.EqualTo("del"));
    }

    [Test]
    public async Task OnGetAsync_UnknownId_ReturnsNotFound()
    {
        _store.Setup(s => s.GetByIdAsync(It.IsAny<string>(), It.IsAny<CancellationToken>()))
              .ReturnsAsync((SourceDefinition?)null);

        var model  = new DeleteModel(_store.Object);
        var result = await model.OnGetAsync("missing");

        Assert.That(result, Is.InstanceOf<NotFoundResult>());
    }

    // ─── OnPostAsync ─────────────────────────────────────────────

    [Test]
    public async Task OnPostAsync_DeletesSourceAndRedirects()
    {
        _store.Setup(s => s.DeleteAsync("del", It.IsAny<CancellationToken>()))
              .Returns(Task.CompletedTask);

        var model  = new DeleteModel(_store.Object);
        var result = await model.OnPostAsync("del");

        Assert.That(result, Is.InstanceOf<RedirectToPageResult>());
        _store.Verify(s => s.DeleteAsync("del", It.IsAny<CancellationToken>()), Times.Once);
    }
}
