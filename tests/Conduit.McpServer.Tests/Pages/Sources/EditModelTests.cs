using Conduit.McpServer.Pages.Sources;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Moq;
using NUnit.Framework;

namespace Conduit.McpServer.Tests.Pages.Sources;

[TestFixture]
public class EditModelTests
{
    private Mock<ISourceConfigStore> _store = null!;
    private Mock<ISyncService>       _sync  = null!;

    [SetUp]
    public void SetUp()
    {
        _store = new Mock<ISourceConfigStore>();
        _sync  = new Mock<ISyncService>();
        _sync.Setup(s => s.SyncAsync(It.IsAny<string>(), It.IsAny<CancellationToken>()))
             .Returns(Task.CompletedTask);
    }

    // ─── OnGetAsync ──────────────────────────────────────────────

    [Test]
    public async Task OnGetAsync_LoadsSourceFromStore()
    {
        var source = new SourceDefinition { Id = "abc", Name = "Docs", Type = SourceTypes.ManualDocument };
        _store.Setup(s => s.GetByIdAsync("abc", It.IsAny<CancellationToken>()))
              .ReturnsAsync(source);

        var model  = new EditModel(_store.Object, _sync.Object);
        var result = await model.OnGetAsync("abc");

        Assert.That(result,          Is.InstanceOf<PageResult>());
        Assert.That(model.Source.Id, Is.EqualTo("abc"));
    }

    [Test]
    public async Task OnGetAsync_UnknownId_ReturnsNotFound()
    {
        _store.Setup(s => s.GetByIdAsync(It.IsAny<string>(), It.IsAny<CancellationToken>()))
              .ReturnsAsync((SourceDefinition?)null);

        var model  = new EditModel(_store.Object, _sync.Object);
        var result = await model.OnGetAsync("bad-id");

        Assert.That(result, Is.InstanceOf<NotFoundResult>());
    }

    // ─── OnPostAsync ─────────────────────────────────────────────

    [Test]
    public async Task OnPostAsync_ValidSource_SavesAndRedirects()
    {
        _store.Setup(s => s.SaveAsync(It.IsAny<SourceDefinition>(), It.IsAny<CancellationToken>()))
              .Returns(Task.CompletedTask);

        var model = new EditModel(_store.Object, _sync.Object);
        model.Source = new SourceDefinition { Id = "x", Name = "Updated", Type = SourceTypes.ManualDocument };

        var result = await model.OnPostAsync(
            configContent:      "C",
            configFile:         null,
            configBaseUrl:      null, configAuthType:     null,
            configPat:          null, configToken:        null,
            configApiKeyHeader: null, configApiKeyValue:  null,
            configUsername:     null, configPassword:     null, configDomain:       null,
            configQuery:        null, configFields:       null,
            configRepository:   null, configBranch:       null, configGlobPatterns: null,
            configPipelineId:   null, configLastNBuilds:  null,
            configWikiName:     null, configPathFilter:   null,
            configUrl:          null, configTitle:        null, configContentType:  null);

        Assert.That(result, Is.InstanceOf<RedirectToPageResult>());
        _store.Verify(s => s.SaveAsync(It.IsAny<SourceDefinition>(), It.IsAny<CancellationToken>()),
            Times.Once);
    }
}
