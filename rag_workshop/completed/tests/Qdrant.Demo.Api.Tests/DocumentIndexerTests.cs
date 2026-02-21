using NUnit.Framework;
using Qdrant.Demo.Api.Extensions;
using Qdrant.Demo.Api.Models;

namespace Qdrant.Demo.Api.Tests;

[TestFixture]
public class DocumentIndexerTests
{
    // ─── Deterministic point-id from explicit Id ────────────────

    [Test]
    public void PointId_IsDeterministic_WhenIdProvided()
    {
        var req = new DocumentUpsertRequest(
            Id: "doc-001",
            Text: "Some content");

        var id1 = req.Id!.ToDeterministicGuid();
        var id2 = req.Id!.ToDeterministicGuid();

        Assert.That(id1, Is.EqualTo(id2));
    }

    [Test]
    public void PointId_FallsBackToTextHash_WhenIdIsNull()
    {
        var req = new DocumentUpsertRequest(
            Id: null,
            Text: "Fallback text content");

        var id = req.Text.ToDeterministicGuid();

        Assert.That(id, Is.Not.EqualTo(Guid.Empty));
        Assert.That(id, Is.EqualTo("Fallback text content".ToDeterministicGuid()));
    }

    [Test]
    public void PointId_DifferentIds_ProduceDifferentPointIds()
    {
        var id1 = "doc-001".ToDeterministicGuid();
        var id2 = "doc-002".ToDeterministicGuid();

        Assert.That(id1, Is.Not.EqualTo(id2));
    }

    [Test]
    public void SameText_SamePointId_WhenNoExplicitId()
    {
        var id1 = "identical text".ToDeterministicGuid();
        var id2 = "identical text".ToDeterministicGuid();

        Assert.That(id1, Is.EqualTo(id2),
            "Re-indexing the same text without an explicit Id should produce the same point-id (idempotent upsert).");
    }

    // ─── Model shape tests ──────────────────────────────────────

    [Test]
    public void DocumentUpsertRequest_TagsAndProperties_AreOptional()
    {
        var req = new DocumentUpsertRequest(Id: null, Text: "hello");

        Assert.That(req.Tags, Is.Null);
        Assert.That(req.Properties, Is.Null);
    }

    [Test]
    public void DocumentUpsertRequest_TagsAndProperties_CanBePopulated()
    {
        var req = new DocumentUpsertRequest(
            Id: "id-1",
            Text: "hello",
            Tags: new Dictionary<string, string> { ["category"] = "science" },
            Properties: new Dictionary<string, string> { ["source_url"] = "https://example.com" });

        Assert.That(req.Tags, Has.Count.EqualTo(1));
        Assert.That(req.Tags!["category"], Is.EqualTo("science"));
        Assert.That(req.Properties, Has.Count.EqualTo(1));
        Assert.That(req.Properties!["source_url"], Is.EqualTo("https://example.com"));
    }

    // ─── Search request defaults ────────────────────────────────

    [Test]
    public void TopKSearchRequest_DefaultK_Is5()
    {
        var req = new TopKSearchRequest(QueryText: "test");
        Assert.That(req.K, Is.EqualTo(5));
        Assert.That(req.Tags, Is.Null);
    }

    [Test]
    public void ThresholdSearchRequest_DefaultThreshold_Is0_4()
    {
        var req = new ThresholdSearchRequest(QueryText: "test");
        Assert.That(req.ScoreThreshold, Is.EqualTo(0.4f));
        Assert.That(req.Limit, Is.EqualTo(100));
    }

    [Test]
    public void MetadataSearchRequest_DefaultLimit_Is25()
    {
        var req = new MetadataSearchRequest();
        Assert.That(req.Limit, Is.EqualTo(25));
        Assert.That(req.Tags, Is.Null);
    }
}
