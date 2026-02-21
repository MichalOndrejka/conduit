using System.Text.Json;
using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class AdoWorkItemQuerySourceTests
{
    private Mock<IAdoClient> _ado  = null!;

    [SetUp]
    public void SetUp() => _ado = new Mock<IAdoClient>();

    // ─── FetchDocumentsAsync ─────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_ReturnsOneDocumentPerWorkItem()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeWorkItems(3));

        var source = MakeSource();
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(3));
    }

    [Test]
    public async Task FetchDocumentsAsync_DocumentIdContainsWorkItemId()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeWorkItems(1, startId: 42));

        var source = MakeSource();
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Id, Is.EqualTo("wi-42"));
    }

    [Test]
    public async Task FetchDocumentsAsync_TagsContainSourceNameAndWorkItemType()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeWorkItems(1));

        var source = MakeSource("Sprint Items");
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Tags["source_name"],    Is.EqualTo("Sprint Items"));
        Assert.That(docs[0].Tags["work_item_type"], Is.EqualTo("Bug"));
    }

    [Test]
    public async Task FetchDocumentsAsync_EmptyQueryResult_ReturnsEmptyList()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(new List<JsonElement>());

        var source = MakeSource();
        var docs   = await source.FetchDocumentsAsync();

        Assert.That(docs, Is.Empty);
    }

    // ─── Source metadata ─────────────────────────────────────────

    [Test]
    public void CollectionName_IsAdoWorkItemsCollection()
    {
        Assert.That(MakeSource().CollectionName, Is.EqualTo(CollectionNames.AdoWorkItems));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private AdoWorkItemQuerySource MakeSource(string name = "Work Items") =>
        new(new SourceDefinition
        {
            Id   = "src",
            Name = name,
            Type = SourceTypes.AdoWorkItemQuery,
            Config = new Dictionary<string, string>
            {
                [ConfigKeys.Organization] = "org",
                [ConfigKeys.Project]      = "proj",
                [ConfigKeys.Pat]          = "token",
                [ConfigKeys.Query]        = "SELECT [System.Id] FROM WorkItems"
            }
        }, _ado.Object);

    private static IReadOnlyList<JsonElement> FakeWorkItems(int count, int startId = 1)
    {
        var items = new List<JsonElement>();
        for (var i = 0; i < count; i++)
        {
            var id  = startId + i;
            var json = $$"""
                {
                  "id": {{id}},
                  "url": "https://dev.azure.com/org/proj/_apis/wit/workItems/{{id}}",
                  "fields": {
                    "System.Title": "Work Item {{id}}",
                    "System.Description": "Description {{id}}",
                    "System.WorkItemType": "Bug",
                    "System.State": "Active"
                  }
                }
                """;
            items.Add(JsonSerializer.Deserialize<JsonElement>(json));
        }
        return items;
    }
}
