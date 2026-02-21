using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class SourceFactoryTests
{
    private SourceFactory _factory = null!;

    [SetUp]
    public void SetUp() => _factory = new SourceFactory(new Mock<IAdoClient>().Object);

    // ─── Correct type per source type string ─────────────────────

    [Test]
    public void Create_ManualDocument_ReturnsManualDocumentSource()
    {
        var def    = MakeDef(SourceTypes.ManualDocument);
        var source = _factory.Create(def);

        Assert.That(source, Is.InstanceOf<ManualDocumentSource>());
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.ManualDocuments));
    }

    [Test]
    public void Create_AdoWorkItemQuery_ReturnsAdoWorkItemQuerySource()
    {
        var source = _factory.Create(MakeDef(SourceTypes.AdoWorkItemQuery));

        Assert.That(source, Is.InstanceOf<AdoWorkItemQuerySource>());
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.AdoWorkItems));
    }

    [Test]
    public void Create_AdoCodeRepo_ReturnsAdoCodeRepoSource()
    {
        var source = _factory.Create(MakeDef(SourceTypes.AdoCodeRepo));

        Assert.That(source, Is.InstanceOf<AdoCodeRepoSource>());
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.AdoCode));
    }

    [Test]
    public void Create_AdoPipelineBuild_ReturnsAdoPipelineBuildSource()
    {
        var source = _factory.Create(MakeDef(SourceTypes.AdoPipelineBuild));

        Assert.That(source, Is.InstanceOf<AdoPipelineBuildSource>());
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.AdoBuilds));
    }

    [Test]
    public void Create_AdoRequirements_ReturnsAdoRequirementsSource()
    {
        var source = _factory.Create(MakeDef(SourceTypes.AdoRequirements));

        Assert.That(source, Is.InstanceOf<AdoRequirementsSource>());
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.AdoRequirements));
    }

    [Test]
    public void Create_AdoTestCase_ReturnsAdoTestCaseSource()
    {
        var source = _factory.Create(MakeDef(SourceTypes.AdoTestCase));

        Assert.That(source, Is.InstanceOf<AdoTestCaseSource>());
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.AdoTestCases));
    }

    [Test]
    public void Create_UnknownType_ThrowsArgumentException()
    {
        var def = MakeDef("unknown-type");

        Assert.Throws<ArgumentException>(() => _factory.Create(def));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private static SourceDefinition MakeDef(string type) => new() { Type = type, Name = "Test" };
}
