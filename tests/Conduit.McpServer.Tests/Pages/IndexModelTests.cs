using Conduit.McpServer.Pages;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Moq;
using NUnit.Framework;

namespace Conduit.McpServer.Tests.Pages;

[TestFixture]
public class IndexModelTests
{
    private Mock<ISourceConfigStore> _store = null!;

    [SetUp]
    public void SetUp() => _store = new Mock<ISourceConfigStore>();

    // ─── OnGetAsync ──────────────────────────────────────────────

    [Test]
    public async Task OnGetAsync_LoadsAllSourcesFromStore()
    {
        var sources = new List<SourceDefinition>
        {
            new() { Id = "a", Name = "Docs",   Type = SourceTypes.ManualDocument },
            new() { Id = "b", Name = "Issues", Type = SourceTypes.AdoWorkItemQuery }
        };
        _store.Setup(s => s.GetAllAsync(It.IsAny<CancellationToken>()))
              .ReturnsAsync(sources);

        var model = new IndexModel(_store.Object);
        await model.OnGetAsync();

        Assert.That(model.Sources, Has.Count.EqualTo(2));
    }

    [Test]
    public async Task OnGetAsync_WhenNoSources_ExposesEmptyList()
    {
        _store.Setup(s => s.GetAllAsync(It.IsAny<CancellationToken>()))
              .ReturnsAsync(new List<SourceDefinition>());

        var model = new IndexModel(_store.Object);
        await model.OnGetAsync();

        Assert.That(model.Sources, Is.Empty);
    }
}
