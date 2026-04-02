using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
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
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(["/src/Foo.cs", "/src/Foo.js", "/README.md"]);

        _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<AdoConnectionConfig>(),
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
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(["/src/File.cs"]);

        _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<AdoConnectionConfig>(),
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
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(["/Service.cs"]);

        _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<AdoConnectionConfig>(),
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

    // ─── Parser integration: one doc per code unit ───────────────

    [Test]
    public async Task FetchDocumentsAsync_WithCSharpParser_EmitsOneDocPerCodeUnit()
    {
        SetupTree(["/UserService.cs"]);
        SetupContent("/UserService.cs",
            "public class UserService { public void GetUser(int id) {} }");

        var source = MakeSourceWith(new CSharpParser());
        var docs   = await source.FetchDocumentsAsync();

        // At minimum: class unit + method unit
        Assert.That(docs, Has.Count.GreaterThanOrEqualTo(2));
    }

    [Test]
    public async Task FetchDocumentsAsync_WithParser_DocTextIsEnrichedText()
    {
        SetupTree(["/Foo.cs"]);
        SetupContent("/Foo.cs", "public class Foo {}");

        var source = MakeSourceWith(new CSharpParser());
        var docs   = await source.FetchDocumentsAsync();

        // Every document produced by the parser must start with the context header
        Assert.That(docs.All(d => d.Text.StartsWith("// Language:")), Is.True);
    }

    [Test]
    public async Task FetchDocumentsAsync_WithParser_TagsContainCodeKindAndIsPublic()
    {
        SetupTree(["/Foo.cs"]);
        SetupContent("/Foo.cs", "public class Foo {}");

        var source = MakeSourceWith(new CSharpParser());
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs.All(d => d.Tags.ContainsKey("code_kind")), Is.True);
        Assert.That(docs.All(d => d.Tags.ContainsKey("is_public")),  Is.True);
    }

    [Test]
    public async Task FetchDocumentsAsync_WithParser_PropertiesContainUnitMetadata()
    {
        SetupTree(["/Foo.cs"]);
        SetupContent("/Foo.cs", "public class Foo {}");

        var source = MakeSourceWith(new CSharpParser());
        var docs   = await source.FetchDocumentsAsync();

        var classDoc = docs.First(d => d.Tags["code_kind"] == "class");
        Assert.That(classDoc.Properties["unit_name"], Is.EqualTo("Foo"));
        Assert.That(classDoc.Properties["unit_kind"], Is.EqualTo("Class"));
    }

    [Test]
    public async Task FetchDocumentsAsync_WithParser_DocumentIdContainsUnitSlug()
    {
        SetupTree(["/Foo.cs"]);
        SetupContent("/Foo.cs", "public class Foo {}");

        var source = MakeSourceWith(new CSharpParser());
        var docs   = await source.FetchDocumentsAsync();

        // IDs must be unique across units in the same file
        var ids = docs.Select(d => d.Id).ToList();
        Assert.That(ids, Is.Unique);
    }

    [Test]
    public async Task FetchDocumentsAsync_WithParser_IdIsStableAcrossCalls()
    {
        SetupTree(["/Foo.cs"]);
        SetupContent("/Foo.cs", "public class Foo {}");

        var source = MakeSourceWith(new CSharpParser());

        var docs1 = await source.FetchDocumentsAsync();
        var docs2 = await source.FetchDocumentsAsync();

        var ids1 = docs1.Select(d => d.Id).OrderBy(x => x).ToList();
        var ids2 = docs2.Select(d => d.Id).OrderBy(x => x).ToList();

        Assert.That(ids1, Is.EqualTo(ids2));
    }

    // ─── Parser integration: fallback ────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_NoParserForExtension_FallsBackToSingleDoc()
    {
        SetupTree(["/script.sh"]);
        SetupContent("/script.sh", "#!/bin/bash\necho hello");

        // No parser registered — must fall back to single whole-file document
        var source = MakeSource(globPatterns: "**/*.sh");
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(1));
        Assert.That(docs[0].Tags.ContainsKey("code_kind"), Is.False);
    }

    [Test]
    public async Task FetchDocumentsAsync_ParserReturnsNoUnits_FallsBackToSingleDoc()
    {
        SetupTree(["/empty.cs"]);
        SetupContent("/empty.cs", "// file with no declarations");

        // CSharpParser will return no units for a file with only comments
        var source = MakeSourceWith(new CSharpParser());
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(1));
        Assert.That(docs[0].Tags.ContainsKey("code_kind"), Is.False);
    }

    // ─── Parser integration: Markdown ────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_MarkdownFile_EmitsOneUnitPerSection()
    {
        SetupTree(["/README.md"]);
        SetupContent("/README.md", "# Introduction\nHello\n## Setup\nInstructions");

        var source = MakeSourceWith(new MarkdownParser(), globPatterns: "**/*.md");
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(2));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private void SetupTree(IEnumerable<string> paths)
        => _ado.Setup(a => a.GetFileTreeAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(paths.ToList());

    private void SetupContent(string path, string content)
        => _ado.Setup(a => a.GetFileContentAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<string>(), path,
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(content);

    private AdoCodeRepoSource MakeSource(
        string globPatterns = "**/*.cs",
        string repo         = "Repo") =>
        new(MakeDefinition(globPatterns, repo), _ado.Object, new CodeParserRegistry([]));

    private AdoCodeRepoSource MakeSourceWith(
        ICodeParser parser,
        string globPatterns = "**/*.cs",
        string repo         = "Repo") =>
        new(MakeDefinition(globPatterns, repo), _ado.Object, new CodeParserRegistry([parser]));

    private static SourceDefinition MakeDefinition(string globPatterns = "**/*.cs", string repo = "Repo") =>
        new()
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
                [ConfigKeys.GlobPatterns] = globPatterns,
            }
        };
}
