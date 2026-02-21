using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Moq;
using NUnit.Framework;
using Qdrant.Client.Grpc;

namespace Conduit.Rag.Tests.Services;

[TestFixture]
public class SearchServiceTests
{
    private Mock<IVectorStore>          _store      = null!;
    private Mock<IEmbeddingService>     _embeddings = null!;
    private Mock<IQdrantFilterFactory>  _filters    = null!;
    private SearchService               _service    = null!;

    [SetUp]
    public void SetUp()
    {
        _store      = new Mock<IVectorStore>();
        _embeddings = new Mock<IEmbeddingService>();
        _filters    = new Mock<IQdrantFilterFactory>();

        _embeddings
            .Setup(e => e.EmbedAsync(It.IsAny<string>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(new float[1536]);

        _filters
            .Setup(f => f.CreateGrpcFilter(It.IsAny<Dictionary<string, string>?>()))
            .Returns((Filter?)null);

        _store
            .Setup(s => s.SearchAsync(
                It.IsAny<string>(), It.IsAny<float[]>(), It.IsAny<ulong>(),
                It.IsAny<Filter?>(), It.IsAny<bool>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(new List<ScoredPoint>());

        _service = new SearchService(_store.Object, _embeddings.Object, _filters.Object);
    }

    // ─── Embedding ────────────────────────────────────────────────

    [Test]
    public async Task SearchAsync_AlwaysEmbedsTheQuery()
    {
        await _service.SearchAsync("collection", "find something");

        _embeddings.Verify(
            e => e.EmbedAsync("find something", It.IsAny<CancellationToken>()),
            Times.Once);
    }

    // ─── Collection routing ───────────────────────────────────────

    [Test]
    public async Task SearchAsync_PassesCollectionNameToStore()
    {
        await _service.SearchAsync("conduit_ado_workitems", "query");

        _store.Verify(s => s.SearchAsync(
            "conduit_ado_workitems",
            It.IsAny<float[]>(),
            It.IsAny<ulong>(),
            It.IsAny<Filter?>(),
            It.IsAny<bool>(),
            It.IsAny<CancellationToken>()),
            Times.Once);
    }

    // ─── TopK ─────────────────────────────────────────────────────

    [Test]
    public async Task SearchAsync_PassesTopKAsLimitToStore()
    {
        await _service.SearchAsync("col", "query", topK: 10);

        _store.Verify(s => s.SearchAsync(
            It.IsAny<string>(),
            It.IsAny<float[]>(),
            10UL,
            It.IsAny<Filter?>(),
            It.IsAny<bool>(),
            It.IsAny<CancellationToken>()),
            Times.Once);
    }

    // ─── Tag filtering ────────────────────────────────────────────

    [Test]
    public async Task SearchAsync_WithTags_BuildsFilterAndPassesToStore()
    {
        var tags   = new Dictionary<string, string> { ["source_name"] = "docs" };
        var filter = new Filter();
        _filters.Setup(f => f.CreateGrpcFilter(tags)).Returns(filter);

        await _service.SearchAsync("col", "query", tags: tags);

        _store.Verify(s => s.SearchAsync(
            It.IsAny<string>(),
            It.IsAny<float[]>(),
            It.IsAny<ulong>(),
            filter,
            It.IsAny<bool>(),
            It.IsAny<CancellationToken>()),
            Times.Once);
    }

    // ─── Result mapping ───────────────────────────────────────────

    [Test]
    public async Task SearchAsync_MapsStoreResultsToSearchResults()
    {
        var point = new ScoredPoint { Id = new PointId { Uuid = "abc" }, Score = 0.9f };
        point.Payload[PayloadKeys.Text] = new Value { StringValue = "result text" };

        _store
            .Setup(s => s.SearchAsync(
                It.IsAny<string>(), It.IsAny<float[]>(), It.IsAny<ulong>(),
                It.IsAny<Filter?>(), It.IsAny<bool>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(new List<ScoredPoint> { point });

        var results = await _service.SearchAsync("col", "query");

        Assert.That(results,           Has.Count.EqualTo(1));
        Assert.That(results[0].Id,     Is.EqualTo("abc"));
        Assert.That(results[0].Score,  Is.EqualTo(0.9f).Within(0.001f));
        Assert.That(results[0].Text,   Is.EqualTo("result text"));
    }
}
