using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class AdoCodeRepoSourceTests
{
    private Mock<IAdoClient> _ado = null!;

    [SetUp]
    public void SetUp() => _ado = new Mock<IAdoClient>();

    // ─── File filtering (glob patterns) ─────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_OnlyIncludesFilesMatchingGlobPattern()
    {
        _ado.Setup(a => a.GetFileTreeAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(["/src/Foo.cs", "/src/Foo.js", "/README.md"]);

        _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<string>(), "/src/Foo.cs",
                It.IsAny<CancellationToken>()))
            .ReturnsAsync("class Foo {}");

        // Pattern: only *.cs files
        var source = MakeSource(globPatterns: "**/*.cs");
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(1));
        Assert.That(docs[0].Properties["path"], Is.EqualTo("/src/Foo.cs"));
    }

    [Test]
    public async Task FetchDocumentsAsync_SkipsEmptyFiles()
    {
        _ado.Setup(a => a.GetFileTreeAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(["/src/File.cs"]);

        _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(string.Empty);

        var source = MakeSource();
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Is.Empty);
    }

    [Test]
    public async Task FetchDocumentsAsync_TagsContainRepositoryAndExtension()
    {
        _ado.Setup(a => a.GetFileTreeAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(["/Service.cs"]);

        _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync("// C# code");

        var source = MakeSource(repo: "MyRepo");
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Tags["repository"], Is.EqualTo("MyRepo"));
        Assert.That(docs[0].Tags["file_ext"],   Is.EqualTo("cs"));
    }

    // ─── Collection metadata ─────────────────────────────────────

    [Test]
    public void CollectionName_IsAdoCodeCollection()
    {
        Assert.That(MakeSource().CollectionName, Is.EqualTo(CollectionNames.AdoCode));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private AdoCodeRepoSource MakeSource(
        string globPatterns = "**/*.cs",
        string repo         = "Repo") =>
        new(new SourceDefinition
        {
            Id   = "src",
            Name = "Code",
            Type = SourceTypes.AdoCodeRepo,
            Config = new Dictionary<string, string>
            {
                [ConfigKeys.Organization] = "org",
                [ConfigKeys.Project]      = "proj",
                [ConfigKeys.Pat]          = "token",
                [ConfigKeys.Repository]   = repo,
                [ConfigKeys.Branch]       = "main",
                [ConfigKeys.GlobPatterns] = globPatterns
            }
        }, _ado.Object);
}
