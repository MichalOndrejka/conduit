using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;
using System.Text.Json;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class AdoWikiSourceTests
{
    private Mock<IAdoClient> _ado = null!;

    [SetUp]
    public void SetUp() => _ado = new Mock<IAdoClient>();

    // ─── Wiki resolution ──────────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_NoWikiName_UsesFirstWiki()
    {
        SetupWikis([MakeWiki("wiki-one", "repo-1"), MakeWiki("wiki-two", "repo-2")]);
        SetupTree("repo-1", []);

        var source = MakeSource();
        await source.FetchDocumentsAsync();

        _ado.Verify(a => a.GetFileTreeAsync(
            It.IsAny<AdoConnectionConfig>(), "repo-1", "wikiMaster",
            It.IsAny<string>(), It.IsAny<CancellationToken>()), Times.Once);
    }

    [Test]
    public async Task FetchDocumentsAsync_NamedWiki_UsesMatchingWiki()
    {
        SetupWikis([MakeWiki("wiki-one", "repo-1"), MakeWiki("wiki-two", "repo-2")]);
        SetupTree("repo-2", []);

        var source = MakeSource(wikiName: "wiki-two");
        await source.FetchDocumentsAsync();

        _ado.Verify(a => a.GetFileTreeAsync(
            It.IsAny<AdoConnectionConfig>(), "repo-2", "wikiMaster",
            It.IsAny<string>(), It.IsAny<CancellationToken>()), Times.Once);
    }

    [Test]
    public void FetchDocumentsAsync_NamedWikiNotFound_Throws()
    {
        SetupWikis([MakeWiki("wiki-one", "repo-1")]);

        var source = MakeSource(wikiName: "nonexistent");

        Assert.ThrowsAsync<InvalidOperationException>(() => source.FetchDocumentsAsync());
    }

    [Test]
    public void FetchDocumentsAsync_NoWikisAtAll_Throws()
    {
        SetupWikis([]);

        var source = MakeSource();

        Assert.ThrowsAsync<InvalidOperationException>(() => source.FetchDocumentsAsync());
    }

    // ─── Path filtering ───────────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_PathFilter_OnlyIncludesMatchingPaths()
    {
        SetupWikis([MakeWiki("wiki", "repo-1")]);
        SetupTree("repo-1", ["/Architecture/Overview.md", "/General/Notes.md"]);
        SetupContent("/Architecture/Overview.md", "# Overview\nContent");

        var source = MakeSource(pathFilter: "/Architecture");
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(1));
        Assert.That(docs[0].Properties["path"], Is.EqualTo("/Architecture/Overview.md"));
    }

    [Test]
    public async Task FetchDocumentsAsync_NoPathFilter_IncludesAllMdFiles()
    {
        SetupWikis([MakeWiki("wiki", "repo-1")]);
        SetupTree("repo-1", ["/Page1.md", "/Page2.md", "/image.png"]);
        SetupContent("/Page1.md", "# P1\nText");
        SetupContent("/Page2.md", "# P2\nText");

        var source = MakeSource();
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(2));
    }

    // ─── Content parsing ──────────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_PageWithHeadings_EmitsOneDocPerSection()
    {
        SetupWikis([MakeWiki("wiki", "repo-1")]);
        SetupTree("repo-1", ["/Guide.md"]);
        SetupContent("/Guide.md", "# Introduction\nHello\n## Setup\nInstructions");

        var source = MakeSourceWith(new MarkdownParser());
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(2));
    }

    [Test]
    public async Task FetchDocumentsAsync_PageWithNoHeadings_EmitsWholeFileAsOneDoc()
    {
        SetupWikis([MakeWiki("wiki", "repo-1")]);
        SetupTree("repo-1", ["/Plain.md"]);
        SetupContent("/Plain.md", "Just some plain text with no headings.");

        var source = MakeSource();
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(1));
        Assert.That(docs[0].Text, Does.Contain("plain text"));
    }

    [Test]
    public async Task FetchDocumentsAsync_EmptyFile_IsSkipped()
    {
        SetupWikis([MakeWiki("wiki", "repo-1")]);
        SetupTree("repo-1", ["/Empty.md"]);
        SetupContent("/Empty.md", string.Empty);

        var source = MakeSource();
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Is.Empty);
    }

    // ─── Collection metadata, tags, and properties ───────────────

    [Test]
    public void CollectionName_IsAdoWikiCollection()
    {
        Assert.That(MakeSource().CollectionName, Is.EqualTo(CollectionNames.AdoWiki));
    }

    [Test]
    public async Task FetchDocumentsAsync_Tags_ContainWikiNameAndSourceName()
    {
        SetupWikis([MakeWiki("my-wiki", "repo-1")]);
        SetupTree("repo-1", ["/Page.md"]);
        SetupContent("/Page.md", "# Section\nContent");

        var source = MakeSourceWith(new MarkdownParser(), wikiName: "my-wiki");
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Tags["wiki_name"],   Is.EqualTo("my-wiki"));
        Assert.That(docs[0].Tags["source_name"], Is.EqualTo("TestWiki"));
    }

    [Test]
    public async Task FetchDocumentsAsync_Properties_ContainPathAndRepository()
    {
        SetupWikis([MakeWiki("wiki", "repo-abc")]);
        SetupTree("repo-abc", ["/Doc.md"]);
        SetupContent("/Doc.md", "# Title\nBody");

        var source = MakeSourceWith(new MarkdownParser());
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Properties["path"],       Is.EqualTo("/Doc.md"));
        Assert.That(docs[0].Properties["repository"], Is.EqualTo("repo-abc"));
    }

    [Test]
    public async Task FetchDocumentsAsync_DocId_IsStableAcrossCalls()
    {
        SetupWikis([MakeWiki("wiki", "repo-1")]);
        SetupTree("repo-1", ["/Stable.md"]);
        SetupContent("/Stable.md", "# Section\nContent");

        var source = MakeSourceWith(new MarkdownParser());
        var docs1  = await source.FetchDocumentsAsync();
        var docs2  = await source.FetchDocumentsAsync();

        Assert.That(docs1[0].Id, Is.EqualTo(docs2[0].Id));
    }

    // ─── Helpers ──────────────────────────────────────────────────

    private static JsonElement MakeWiki(string name, string repositoryId)
    {
        var json = $"{{\"name\":\"{name}\",\"repositoryId\":\"{repositoryId}\"}}";
        return JsonDocument.Parse(json).RootElement;
    }

    private void SetupWikis(IEnumerable<JsonElement> wikis)
        => _ado.Setup(a => a.GetWikisAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(wikis.ToList());

    private void SetupTree(string repositoryId, IEnumerable<string> paths)
        => _ado.Setup(a => a.GetFileTreeAsync(
                It.IsAny<AdoConnectionConfig>(), repositoryId, "wikiMaster",
                It.IsAny<string>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(paths.ToList());

    private void SetupContent(string path, string content)
        => _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<string>(), path,
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(content);

    private AdoWikiSource MakeSource(string? wikiName = null, string? pathFilter = null)
    {
        var config = new Dictionary<string, string>
        {
            [ConfigKeys.BaseUrl]  = "https://ado.example.com/Project",
            [ConfigKeys.AuthType] = "none",
        };
        if (wikiName   is not null) config[ConfigKeys.WikiName]   = wikiName;
        if (pathFilter is not null) config[ConfigKeys.PathFilter]  = pathFilter;

        return new AdoWikiSource(
            new SourceDefinition { Id = "w1", Name = "TestWiki", Type = SourceTypes.AdoWiki, Config = config },
            _ado.Object,
            new CodeParserRegistry([]));
    }

    private AdoWikiSource MakeSourceWith(ICodeParser parser, string? wikiName = null, string? pathFilter = null)
    {
        var config = new Dictionary<string, string>
        {
            [ConfigKeys.BaseUrl]  = "https://ado.example.com/Project",
            [ConfigKeys.AuthType] = "none",
        };
        if (wikiName   is not null) config[ConfigKeys.WikiName]   = wikiName;
        if (pathFilter is not null) config[ConfigKeys.PathFilter]  = pathFilter;

        return new AdoWikiSource(
            new SourceDefinition { Id = "w1", Name = "TestWiki", Type = SourceTypes.AdoWiki, Config = config },
            _ado.Object,
            new CodeParserRegistry([parser]));
    }
}
