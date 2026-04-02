using System.Text.Json;
using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class AdoTestCaseSourceTests
{
    private Mock<IAdoClient> _ado = null!;

    [SetUp]
    public void SetUp() => _ado = new Mock<IAdoClient>();

    // ─── FetchDocumentsAsync ─────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_DocumentIdContainsTestCaseId()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeTestCases(1, startId: 99));

        var docs = await MakeSource().FetchDocumentsAsync();

        Assert.That(docs[0].Id, Is.EqualTo("tc-99"));
    }

    [Test]
    public async Task FetchDocumentsAsync_TitleIncludedInText()
    {
        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(FakeTestCases(1));

        var docs = await MakeSource().FetchDocumentsAsync();

        Assert.That(docs[0].Text, Does.Contain("Test Case 1"));
    }

    // ─── Test step XML parsing ────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_StepsXml_IsStrippedToPlainText()
    {
        var testCase = FakeTestCaseWithSteps(1,
            stepsXml: "<steps><step><parameterizedString>Click the button</parameterizedString><parameterizedString isformatted='true'>Button is highlighted</parameterizedString></step></steps>");

        _ado.Setup(a => a.RunWorkItemQueryAsync(
                It.IsAny<AdoConnectionConfig>(),
                It.IsAny<string>(), It.IsAny<IReadOnlyList<string>>(),
                It.IsAny<CancellationToken>()))
            .ReturnsAsync(new List<JsonElement> { testCase });

        var docs = await MakeSource().FetchDocumentsAsync();

        // XML tags stripped, plain text content preserved
        Assert.That(docs[0].Text, Does.Contain("Click the button"));
        Assert.That(docs[0].Text, Does.Not.Contain("<step>"));
    }

    // ─── Collection metadata ─────────────────────────────────────

    [Test]
    public void CollectionName_IsAdoTestCasesCollection()
    {
        Assert.That(MakeSource().CollectionName, Is.EqualTo(CollectionNames.AdoTestCases));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private AdoTestCaseSource MakeSource() =>
        new(new SourceDefinition
        {
            Id   = "src",
            Name = "Tests",
            Type = SourceTypes.AdoTestCase,
            Config = new Dictionary<string, string>
            {
                [ConfigKeys.Organization] = "org",
                [ConfigKeys.Project]      = "proj",
                [ConfigKeys.Pat]          = "token",
                [ConfigKeys.Query]        = "SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = 'Test Case'"
            }
        }, _ado.Object);

    private static IReadOnlyList<JsonElement> FakeTestCases(int count, int startId = 1)
    {
        var items = new List<JsonElement>();
        for (var i = 0; i < count; i++)
        {
            var id = startId + i;
            items.Add(FakeTestCaseWithSteps(id, $"Test Case {id}", stepsXml: ""));
        }
        return items;
    }

    private static JsonElement FakeTestCaseWithSteps(int id, string title = "Test Case", string stepsXml = "")
    {
        var json = $$"""
            {
              "id": {{id}},
              "url": "https://dev.azure.com/org/proj/_apis/wit/workItems/{{id}}",
              "fields": {
                "System.Title": "{{title}}",
                "System.State": "Ready",
                "Microsoft.VSTS.TCM.Steps": "{{stepsXml.Replace("\"", "\\\"")}}",
                "Microsoft.VSTS.Common.AutomationStatus": "Not Automated"
              }
            }
            """;
        return JsonSerializer.Deserialize<JsonElement>(json);
    }
}
