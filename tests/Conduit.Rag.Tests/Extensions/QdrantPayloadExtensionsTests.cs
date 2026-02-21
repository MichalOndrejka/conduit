using Conduit.Rag.Extensions;
using Conduit.Rag.Models;
using NUnit.Framework;
using Qdrant.Client.Grpc;

namespace Conduit.Rag.Tests.Extensions;

[TestFixture]
public class QdrantPayloadExtensionsTests
{
    // ─── ToSearchResult ─────────────────────────────────────────

    [Test]
    public void ToSearchResult_ExtractsTextFromPayload()
    {
        var point = MakePoint("abc", 0.9f, text: "hello world");

        var result = point.ToSearchResult();

        Assert.That(result.Text, Is.EqualTo("hello world"));
    }

    [Test]
    public void ToSearchResult_ExtractsScoreAndId()
    {
        var point = MakePoint("my-uuid", 0.75f, text: "content");

        var result = point.ToSearchResult();

        Assert.That(result.Id, Is.EqualTo("my-uuid"));
        Assert.That(result.Score, Is.EqualTo(0.75f).Within(0.001f));
    }

    [Test]
    public void ToSearchResult_ParsesTagsFromPrefixedFields()
    {
        var point = MakePoint("id", 1f, text: "x");
        point.Payload["tag_source_name"] = new Value { StringValue = "docs" };
        point.Payload["tag_state"]       = new Value { StringValue = "Active" };

        var result = point.ToSearchResult();

        Assert.That(result.Tags["source_name"], Is.EqualTo("docs"));
        Assert.That(result.Tags["state"],        Is.EqualTo("Active"));
    }

    [Test]
    public void ToSearchResult_ParsesPropertiesFromPrefixedFields()
    {
        var point = MakePoint("id", 1f, text: "x");
        point.Payload["prop_url"] = new Value { StringValue = "https://example.com" };
        point.Payload["prop_id"]  = new Value { StringValue = "1234" };

        var result = point.ToSearchResult();

        Assert.That(result.Properties["url"], Is.EqualTo("https://example.com"));
        Assert.That(result.Properties["id"],  Is.EqualTo("1234"));
    }

    [Test]
    public void ToSearchResult_DoesNotIncludeInternalFieldsInTagsOrProperties()
    {
        var point = MakePoint("id", 1f, text: "content");
        // indexed_at_ms and chunk metadata should not appear in Tags or Properties
        point.Payload[PayloadKeys.IndexedAtMs] = new Value { IntegerValue = 123456L };
        point.Payload[PayloadKeys.ChunkIndex]  = new Value { StringValue  = "0" };

        var result = point.ToSearchResult();

        Assert.That(result.Tags,       Does.Not.ContainKey("indexed_at_ms"));
        Assert.That(result.Properties, Does.Not.ContainKey("chunk_index"));
    }

    // ─── ToSearchResults ────────────────────────────────────────

    [Test]
    public void ToSearchResults_ReturnsOneResultPerPoint()
    {
        var points = new List<ScoredPoint>
        {
            MakePoint("a", 0.9f, text: "first"),
            MakePoint("b", 0.8f, text: "second"),
            MakePoint("c", 0.7f, text: "third")
        };

        var results = points.ToSearchResults();

        Assert.That(results, Has.Count.EqualTo(3));
        Assert.That(results[0].Text, Is.EqualTo("first"));
        Assert.That(results[2].Id,   Is.EqualTo("c"));
    }

    // ─── Helpers ────────────────────────────────────────────────

    private static ScoredPoint MakePoint(string uuid, float score, string text)
    {
        var point = new ScoredPoint
        {
            Id    = new PointId { Uuid = uuid },
            Score = score
        };
        point.Payload[PayloadKeys.Text] = new Value { StringValue = text };
        return point;
    }
}
