using System.Text.Json;
using Conduit.McpServer.Tools;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Moq;
using NUnit.Framework;

namespace Conduit.McpServer.Tests.Tools;

/// <summary>
/// Tests for all 6 MCP search tools. Each tool is a thin wrapper over ISearchService,
/// so tests verify: correct collection routing, topK forwarding, sourceName filtering,
/// and JSON serialization of results.
/// </summary>
[TestFixture]
public class SearchToolsTests
{
    private Mock<ISearchService> _search = null!;

    [SetUp]
    public void SetUp()
    {
        _search = new Mock<ISearchService>();
        _search
            .Setup(s => s.SearchAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<int>(),
                It.IsAny<Dictionary<string, string>?>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(new List<SearchResult>());
    }

    // ─── ManualDocumentsTools ────────────────────────────────────

    [Test]
    public async Task ManualDocuments_Search_RoutesToCorrectCollection()
    {
        await ManualDocumentsTools.Search(_search.Object, "query");

        VerifyCollection(CollectionNames.ManualDocuments);
    }

    [Test]
    public async Task ManualDocuments_Search_WithSourceName_BuildsTagFilter()
    {
        await ManualDocumentsTools.Search(_search.Object, "query", sourceName: "Arch Docs");

        VerifyTagFilter("source_name", "Arch Docs");
    }

    [Test]
    public async Task ManualDocuments_Search_ReturnsJsonString()
    {
        var result = await ManualDocumentsTools.Search(_search.Object, "query");

        Assert.DoesNotThrow(() => JsonDocument.Parse(result));
    }

    // ─── AdoWorkItemsTools ───────────────────────────────────────

    [Test]
    public async Task AdoWorkItems_Search_RoutesToCorrectCollection()
    {
        await AdoWorkItemsTools.Search(_search.Object, "query");

        VerifyCollection(CollectionNames.AdoWorkItems);
    }

    [Test]
    public async Task AdoWorkItems_Search_ForwardsTopK()
    {
        await AdoWorkItemsTools.Search(_search.Object, "query", topK: 10);

        _search.Verify(s => s.SearchAsync(
            It.IsAny<string>(), It.IsAny<string>(), 10,
            It.IsAny<Dictionary<string, string>?>(), It.IsAny<CancellationToken>()),
            Times.Once);
    }

    // ─── AdoCodeTools ────────────────────────────────────────────

    [Test]
    public async Task AdoCode_Search_RoutesToCorrectCollection()
    {
        await AdoCodeTools.Search(_search.Object, "query");

        VerifyCollection(CollectionNames.AdoCode);
    }

    [Test]
    public async Task AdoCode_Search_WithoutSourceName_PassesNullTags()
    {
        await AdoCodeTools.Search(_search.Object, "query", sourceName: null);

        _search.Verify(s => s.SearchAsync(
            It.IsAny<string>(), It.IsAny<string>(), It.IsAny<int>(),
            null, It.IsAny<CancellationToken>()),
            Times.Once);
    }

    // ─── AdoPipelineBuildsTools ──────────────────────────────────

    [Test]
    public async Task AdoBuilds_Search_RoutesToCorrectCollection()
    {
        await AdoPipelineBuildsTools.Search(_search.Object, "failed test");

        VerifyCollection(CollectionNames.AdoBuilds);
    }

    // ─── AdoRequirementsTools ────────────────────────────────────

    [Test]
    public async Task AdoRequirements_Search_RoutesToCorrectCollection()
    {
        await AdoRequirementsTools.Search(_search.Object, "authentication requirement");

        VerifyCollection(CollectionNames.AdoRequirements);
    }

    [Test]
    public async Task AdoRequirements_Search_WithSourceName_BuildsTagFilter()
    {
        await AdoRequirementsTools.Search(_search.Object, "query", sourceName: "SW Reqs");

        VerifyTagFilter("source_name", "SW Reqs");
    }

    // ─── AdoTestCasesTools ───────────────────────────────────────

    [Test]
    public async Task AdoTestCases_Search_RoutesToCorrectCollection()
    {
        await AdoTestCasesTools.Search(_search.Object, "login test");

        VerifyCollection(CollectionNames.AdoTestCases);
    }

    [Test]
    public async Task AdoTestCases_Search_ReturnsSerializedResults()
    {
        var results = new List<SearchResult>
        {
            new("id-1", 0.95f, "Test case text",
                Tags:       new Dictionary<string, string> { ["source_name"] = "Tests" },
                Properties: new Dictionary<string, string> { ["id"] = "123" })
        };
        _search
            .Setup(s => s.SearchAsync(
                CollectionNames.AdoTestCases, It.IsAny<string>(), It.IsAny<int>(),
                It.IsAny<Dictionary<string, string>?>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(results);

        var json = await AdoTestCasesTools.Search(_search.Object, "login test");
        var doc  = JsonDocument.Parse(json);

        Assert.That(doc.RootElement.GetArrayLength(), Is.EqualTo(1));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private void VerifyCollection(string expectedCollection) =>
        _search.Verify(s => s.SearchAsync(
            expectedCollection, It.IsAny<string>(), It.IsAny<int>(),
            It.IsAny<Dictionary<string, string>?>(), It.IsAny<CancellationToken>()),
            Times.Once);

    private void VerifyTagFilter(string key, string value) =>
        _search.Verify(s => s.SearchAsync(
            It.IsAny<string>(), It.IsAny<string>(), It.IsAny<int>(),
            It.Is<Dictionary<string, string>?>(d => d != null && d[key] == value),
            It.IsAny<CancellationToken>()),
            Times.Once);
}
