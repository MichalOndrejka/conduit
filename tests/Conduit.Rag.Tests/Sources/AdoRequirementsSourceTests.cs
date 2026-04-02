using System.Text.Json;
using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class AdoRequirementsSourceTests
{
    private Mock<IAdoClient> _ado = null!;

    [SetUp]
    public void SetUp() => _ado = new Mock<IAdoClient>();

    // ─── FetchDocumentsAsync ─────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_DocumentIdContainsRequirementId()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeRequirements(1, startId: 55));

        var docs = await MakeSource().FetchDocumentsAsync();

        Assert.That(docs[0].Id, Is.EqualTo("req-55"));
    }

    [Test]
    public async Task FetchDocumentsAsync_TagsContainRequirementType()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeRequirements(1));

        var docs = await MakeSource("Reqs").FetchDocumentsAsync();

        Assert.That(docs[0].Tags["source_name"],      Is.EqualTo("Reqs"));
        Assert.That(docs[0].Tags["requirement_type"], Is.EqualTo("Requirement"));
    }

    [Test]
    public void CollectionName_IsAdoRequirementsCollection()
    {
        Assert.That(MakeSource().CollectionName, Is.EqualTo(CollectionNames.AdoRequirements));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private AdoRequirementsSource MakeSource(string name = "Requirements") =>
        new(new SourceDefinition
        {
            Id   = "src",
            Name = name,
            Type = SourceTypes.AdoRequirements,
            Config = new Dictionary<string, string>
            {
                [ConfigKeys.Organization] = "org",
                [ConfigKeys.Project]      = "proj",
                [ConfigKeys.Pat]          = "token",
                [ConfigKeys.Query]        = "SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = 'Requirement'"
            }
        }, _ado.Object);

    private static IReadOnlyList<JsonElement> FakeRequirements(int count, int startId = 1)
    {
        var items = new List<JsonElement>();
        for (var i = 0; i < count; i++)
        {
            var id   = startId + i;
            var json = $$"""
                {
                  "id": {{id}},
                  "url": "https://dev.azure.com/org/proj/_apis/wit/workItems/{{id}}",
                  "fields": {
                    "System.Title": "Requirement {{id}}",
                    "System.Description": "The system shall...",
                    "System.WorkItemType": "Requirement",
                    "System.State": "Active"
                  }
                }
                """;
            items.Add(JsonSerializer.Deserialize<JsonElement>(json));
        }
        return items;
    }
}
