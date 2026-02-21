using NUnit.Framework;
using Qdrant.Demo.Api.Models;
using static Qdrant.Demo.Api.Models.PayloadKeys;

namespace Qdrant.Demo.Api.Tests;

[TestFixture]
public class ChatTests
{
    // ─── ChatRequest defaults ───────────────────────────────────

    [Test]
    public void ChatRequest_DefaultK_Is5()
    {
        var req = new ChatRequest(Question: "what is RAG?");
        Assert.That(req.K, Is.EqualTo(5));
    }

    [Test]
    public void ChatRequest_DefaultScoreThreshold_IsNull()
    {
        var req = new ChatRequest(Question: "test");
        Assert.That(req.ScoreThreshold, Is.Null);
    }

    [Test]
    public void ChatRequest_DefaultTags_IsNull()
    {
        var req = new ChatRequest(Question: "test");
        Assert.That(req.Tags, Is.Null);
    }

    [Test]
    public void ChatRequest_DefaultSystemPrompt_IsNull()
    {
        var req = new ChatRequest(Question: "test");
        Assert.That(req.SystemPrompt, Is.Null);
    }

    [Test]
    public void ChatRequest_AllFieldsCanBeSet()
    {
        var tags = new Dictionary<string, string> { ["category"] = "science" };

        var req = new ChatRequest(
            Question: "what is photosynthesis?",
            K: 3,
            ScoreThreshold: 0.5f,
            Tags: tags,
            SystemPrompt: "Custom prompt");

        Assert.That(req.Question, Is.EqualTo("what is photosynthesis?"));
        Assert.That(req.K, Is.EqualTo(3));
        Assert.That(req.ScoreThreshold, Is.EqualTo(0.5f));
        Assert.That(req.Tags, Has.Count.EqualTo(1));
        Assert.That(req.SystemPrompt, Is.EqualTo("Custom prompt"));
    }

    // ─── ChatSource shape ───────────────────────────────────────

    [Test]
    public void ChatSource_RecordShape()
    {
        var source = new ChatSource(
            Id: "abc-123",
            Score: 0.87f,
            TextSnippet: "Some relevant text");

        Assert.That(source.Id, Is.EqualTo("abc-123"));
        Assert.That(source.Score, Is.EqualTo(0.87f));
        Assert.That(source.TextSnippet, Is.EqualTo("Some relevant text"));
    }

    // ─── ChatResponse shape ─────────────────────────────────────

    [Test]
    public void ChatResponse_RecordShape()
    {
        var sources = new List<ChatSource>
        {
            new("id-1", 0.9f, "text-1"),
            new("id-2", 0.7f, "text-2")
        };

        var response = new ChatResponse(
            Answer: "The answer is 42.",
            Sources: sources);

        Assert.That(response.Answer, Is.EqualTo("The answer is 42."));
        Assert.That(response.Sources, Has.Count.EqualTo(2));
    }

    // ─── SearchHit shape ────────────────────────────────────────

    [Test]
    public void SearchHit_RecordShape()
    {
        var payload = new Dictionary<string, object?>
        {
            [Text] = "some text",
            ["tag_category"] = "science"
        };

        var hit = new SearchHit(
            Id: "abc-123",
            Score: 0.85f,
            Payload: payload);

        Assert.That(hit.Id, Is.EqualTo("abc-123"));
        Assert.That(hit.Score, Is.EqualTo(0.85f));
        Assert.That(hit.Payload, Has.Count.EqualTo(2));
        Assert.That(hit.Payload[Text], Is.EqualTo("some text"));
    }

    // ─── PayloadKeys constants ──────────────────────────────────

    [Test]
    public void PayloadKeys_Text_IsCorrect()
    {
        Assert.That(Text, Is.EqualTo("text"));
    }

    [Test]
    public void PayloadKeys_Prefixes_AreCorrect()
    {
        Assert.That(TagPrefix, Is.EqualTo("tag_"));
        Assert.That(PropertyPrefix, Is.EqualTo("prop_"));
    }
}
