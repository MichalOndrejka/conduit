using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class ManualDocumentSourceTests
{
    // ─── FetchDocumentsAsync ─────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_ReturnsSingleDocument()
    {
        var source = MakeSource("Hello, world!", "My Doc");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(1));
    }

    [Test]
    public async Task FetchDocumentsAsync_DocumentTextMatchesContent()
    {
        var source = MakeSource("Important architecture notes.", "Arch Doc");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Text, Is.EqualTo("Important architecture notes."));
    }

    [Test]
    public async Task FetchDocumentsAsync_TagsContainSourceName()
    {
        var source = MakeSource("content", "My Docs");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Tags["source_name"], Is.EqualTo("My Docs"));
    }

    [Test]
    public async Task FetchDocumentsAsync_PropertiesContainTitle()
    {
        var def = new SourceDefinition
        {
            Id   = "x",
            Name = "Docs",
            Type = SourceTypes.ManualDocument,
            Config = new Dictionary<string, string>
            {
                [ConfigKeys.Content] = "content",
                [ConfigKeys.Title]   = "Architecture Overview"
            }
        };
        var source = new ManualDocumentSource(def);

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Properties["title"], Is.EqualTo("Architecture Overview"));
    }

    // ─── Source metadata ─────────────────────────────────────────

    [Test]
    public void Type_IsManualDocument()
    {
        var source = MakeSource("text", "Name");
        Assert.That(source.Type, Is.EqualTo(SourceTypes.ManualDocument));
    }

    [Test]
    public void CollectionName_IsManualDocumentsCollection()
    {
        var source = MakeSource("text", "Name");
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.ManualDocuments));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private static ManualDocumentSource MakeSource(string content, string name) =>
        new(new SourceDefinition
        {
            Id   = "test-id",
            Name = name,
            Type = SourceTypes.ManualDocument,
            Config = new Dictionary<string, string>
            {
                [ConfigKeys.Content] = content
            }
        });
}
