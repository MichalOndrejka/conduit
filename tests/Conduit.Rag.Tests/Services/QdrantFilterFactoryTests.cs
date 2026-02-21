using Conduit.Rag.Services;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Services;

[TestFixture]
public class QdrantFilterFactoryTests
{
    private readonly QdrantFilterFactory _factory = new();

    // ─── CreateGrpcFilter ───────────────────────────────────────

    [Test]
    public void CreateGrpcFilter_ReturnsNull_WhenTagsIsNull()
    {
        var result = _factory.CreateGrpcFilter(null);

        Assert.That(result, Is.Null);
    }

    [Test]
    public void CreateGrpcFilter_ReturnsNull_WhenTagsIsEmpty()
    {
        var result = _factory.CreateGrpcFilter(new Dictionary<string, string>());

        Assert.That(result, Is.Null);
    }

    [Test]
    public void CreateGrpcFilter_SingleTag_CreatesOneMustCondition()
    {
        var tags = new Dictionary<string, string> { ["source_name"] = "docs" };

        var result = _factory.CreateGrpcFilter(tags);

        Assert.That(result,       Is.Not.Null);
        Assert.That(result!.Must, Has.Count.EqualTo(1));
    }

    [Test]
    public void CreateGrpcFilter_MultipleTags_EachBecomesASeparateMustCondition()
    {
        var tags = new Dictionary<string, string>
        {
            ["source_name"] = "docs",
            ["state"]       = "Active",
            ["type"]        = "Bug"
        };

        var result = _factory.CreateGrpcFilter(tags);

        Assert.That(result,       Is.Not.Null);
        Assert.That(result!.Must, Has.Count.EqualTo(3),
            "Each tag should be a separate condition in Must — AND semantics");
    }
}
