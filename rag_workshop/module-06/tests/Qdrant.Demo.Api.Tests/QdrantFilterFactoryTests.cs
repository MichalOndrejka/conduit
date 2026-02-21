using NUnit.Framework;
using Qdrant.Demo.Api.Services;

namespace Qdrant.Demo.Api.Tests;

[TestFixture]
public class QdrantFilterFactoryTests
{
    private readonly QdrantFilterFactory _factory = new();

    // ─── CreateScrollFilter ─────────────────────────────────────

    [Test]
    public void CreateScrollFilter_ReturnsNull_WhenTagsIsNull()
    {
        var result = _factory.CreateScrollFilter(null);
        Assert.That(result, Is.Null);
    }

    [Test]
    public void CreateScrollFilter_ReturnsNull_WhenTagsIsEmpty()
    {
        var result = _factory.CreateScrollFilter(
            new Dictionary<string, string>());
        Assert.That(result, Is.Null);
    }

    [Test]
    public void CreateScrollFilter_ReturnsFilter_WhenTagsProvided()
    {
        var tags = new Dictionary<string, string>
        {
            ["category"] = "science",
            ["language"] = "en"
        };

        var result = _factory.CreateScrollFilter(tags);

        Assert.That(result, Is.Not.Null);
        var json = System.Text.Json.JsonSerializer.Serialize(result);
        Assert.That(json, Does.Contain("tag_category"));
        Assert.That(json, Does.Contain("tag_language"));
        Assert.That(json, Does.Contain("science"));
        Assert.That(json, Does.Contain("en"));
    }

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
        var result = _factory.CreateGrpcFilter(
            new Dictionary<string, string>());
        Assert.That(result, Is.Null);
    }

    [Test]
    public void CreateGrpcFilter_ReturnsFilter_WhenSingleTagProvided()
    {
        var tags = new Dictionary<string, string>
        {
            ["category"] = "science"
        };

        var result = _factory.CreateGrpcFilter(tags);

        Assert.That(result, Is.Not.Null);
        Assert.That(result!.Must, Has.Count.EqualTo(1));
    }

    [Test]
    public void CreateGrpcFilter_AddsEachTagAsSeparateCondition()
    {
        var tags = new Dictionary<string, string>
        {
            ["category"] = "science",
            ["language"] = "en",
            ["author"] = "Jane"
        };

        var result = _factory.CreateGrpcFilter(tags);

        Assert.That(result, Is.Not.Null);
        Assert.That(result!.Must, Has.Count.EqualTo(3),
            "Each tag should be a separate condition in Must — not combined with & operator.");
    }
}
