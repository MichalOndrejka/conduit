using Conduit.McpServer.Pages.Sources;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Moq;
using NUnit.Framework;

namespace Conduit.McpServer.Tests.Pages.Sources;

[TestFixture]
public class CreateModelTests
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

    // ─── OnGet ───────────────────────────────────────────────────

    [Test]
    public void OnGet_WithoutType_LeavesSelectedTypeNull()
    {
        var model = new CreateModel(_store.Object, _sync.Object);
        model.OnGet(type: null);

        Assert.That(model.SelectedType, Is.Null);
    }

    [Test]
    public void OnGet_WithType_SetsSelectedTypeOnModel()
    {
        var model = new CreateModel(_store.Object, _sync.Object);
        model.OnGet(type: SourceTypes.ManualDocument);

        Assert.That(model.SelectedType, Is.EqualTo(SourceTypes.ManualDocument));
        Assert.That(model.Source.Type,  Is.EqualTo(SourceTypes.ManualDocument));
    }

    // ─── OnPostAsync ─────────────────────────────────────────────

    [Test]
    public async Task OnPostAsync_ValidManualSource_SavesAndRedirects()
    {
        _store.Setup(s => s.SaveAsync(It.IsAny<SourceDefinition>(), It.IsAny<CancellationToken>()))
              .Returns(Task.CompletedTask);

        var model = new CreateModel(_store.Object, _sync.Object);
        model.Source = new SourceDefinition
        {
            Id   = "new-id",
            Name = "My Docs",
            Type = SourceTypes.ManualDocument
        };

        var result = await model.OnPostAsync(
            configTitle:         "Title",
            configContent:       "Some content",
            configOrganization:  null, configProject:      null, configPat:         null,
            configQuery:         null, configFields:       null,
            configRepository:    null, configBranch:       null, configGlobPatterns: null,
            configPipelineId:    null, configLastNBuilds:  null);

        Assert.That(result, Is.InstanceOf<RedirectToPageResult>());
        _store.Verify(s => s.SaveAsync(It.IsAny<SourceDefinition>(), It.IsAny<CancellationToken>()),
            Times.Once);
    }

    [Test]
    public async Task OnPostAsync_MissingName_ReturnsPageWithError()
    {
        var model = new CreateModel(_store.Object, _sync.Object);
        model.Source = new SourceDefinition
        {
            Id   = "new-id",
            Name = string.Empty,    // invalid
            Type = SourceTypes.ManualDocument
        };

        var result = await model.OnPostAsync(
            configTitle: null, configContent: "content",
            configOrganization: null, configProject: null, configPat: null,
            configQuery: null, configFields: null,
            configRepository: null, configBranch: null, configGlobPatterns: null,
            configPipelineId: null, configLastNBuilds: null);

        Assert.That(result, Is.InstanceOf<PageResult>());
        _store.Verify(s => s.SaveAsync(It.IsAny<SourceDefinition>(), It.IsAny<CancellationToken>()),
            Times.Never);
    }

    // ─── Source type list ────────────────────────────────────────

    [Test]
    public void SourceTypes_ContainsAllSixTypes()
    {
        var model = new CreateModel(_store.Object, _sync.Object);

        Assert.That(model.SourceTypes, Has.Count.EqualTo(6));
    }
}
