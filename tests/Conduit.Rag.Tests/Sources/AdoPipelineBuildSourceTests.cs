using System.Text.Json;
using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class AdoPipelineBuildSourceTests
{
    private Mock<IAdoClient> _ado = null!;

    [SetUp]
    public void SetUp()
    {
        _ado = new Mock<IAdoClient>();

        // Default: no timeline records
        _ado.Setup(a => a.GetBuildTimelineAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(new List<JsonElement>());
    }

    // ─── FetchDocumentsAsync ─────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_ReturnsOneDocumentPerBuild()
    {
        _ado.Setup(a => a.GetBuildsAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<int>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeBuilds(3));

        var docs = await MakeSource().FetchDocumentsAsync();

        Assert.That(docs, Has.Count.EqualTo(3));
    }

    [Test]
    public async Task FetchDocumentsAsync_DocumentIdContainsBuildId()
    {
        _ado.Setup(a => a.GetBuildsAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<int>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeBuilds(1, startId: 100));

        var docs = await MakeSource().FetchDocumentsAsync();

        Assert.That(docs[0].Id, Is.EqualTo("build-100"));
    }

    [Test]
    public async Task FetchDocumentsAsync_TagsContainPipelineIdAndResult()
    {
        _ado.Setup(a => a.GetBuildsAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<int>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeBuilds(1));

        var docs = await MakeSource(pipelineId: "42").FetchDocumentsAsync();

        Assert.That(docs[0].Tags["pipeline_id"],  Is.EqualTo("42"));
        Assert.That(docs[0].Tags["build_result"], Is.EqualTo("succeeded"));
    }

    // ─── Collection metadata ─────────────────────────────────────

    [Test]
    public void CollectionName_IsAdoBuildsCollection()
    {
        Assert.That(MakeSource().CollectionName, Is.EqualTo(CollectionNames.AdoBuilds));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private AdoPipelineBuildSource MakeSource(string pipelineId = "1") =>
        new(new SourceDefinition
        {
            Id   = "src",
            Name = "CI",
            Type = SourceTypes.AdoPipelineBuild,
            Config = new Dictionary<string, string>
            {
                [ConfigKeys.Organization] = "org",
                [ConfigKeys.Project]      = "proj",
                [ConfigKeys.Pat]          = "token",
                [ConfigKeys.PipelineId]   = pipelineId,
                [ConfigKeys.LastNBuilds]  = "5"
            }
        }, _ado.Object);

    private static IReadOnlyList<JsonElement> FakeBuilds(int count, int startId = 1)
    {
        var items = new List<JsonElement>();
        for (var i = 0; i < count; i++)
        {
            var id   = startId + i;
            var json = $$"""
                {
                  "id": {{id}},
                  "buildNumber": "{{id}}.0",
                  "status": "completed",
                  "result": "succeeded",
                  "startTime": "2025-01-01T10:00:00Z",
                  "finishTime": "2025-01-01T10:05:00Z"
                }
                """;
            items.Add(JsonSerializer.Deserialize<JsonElement>(json));
        }
        return items;
    }
}
