using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Moq;
using NUnit.Framework;
using Qdrant.Client.Grpc;

namespace Conduit.Rag.Tests.Services;

[TestFixture]
public class DocumentIndexerTests
{
    private Mock<IVectorStore>      _store     = null!;
    private Mock<IEmbeddingService> _embeddings = null!;
    private ITextChunker            _chunker    = null!;
    private DocumentIndexer         _indexer    = null!;

    [SetUp]
    public void SetUp()
    {
        _store      = new Mock<IVectorStore>();
        _embeddings = new Mock<IEmbeddingService>();
        _chunker    = new TextChunker(new ChunkingOptions { MaxChunkSize = 100, Overlap = 10 });

        _embeddings
            .Setup(e => e.EmbedAsync(It.IsAny<string>(), It.IsAny<CancellationToken>()))
            .ReturnsAsync(new float[1536]);

        _store
            .Setup(s => s.UpsertAsync(
                It.IsAny<string>(),
                It.IsAny<IReadOnlyList<PointStruct>>(),
                It.IsAny<bool>(),
                It.IsAny<CancellationToken>()))
            .Returns(Task.CompletedTask);

        _indexer = new DocumentIndexer(_store.Object, _embeddings.Object, _chunker);
    }

    // ─── Embedding calls ─────────────────────────────────────────

    [Test]
    public async Task IndexAsync_ShortDocument_CallsEmbedOnce()
    {
        var doc = MakeDocument("short text");

        await _indexer.IndexAsync("collection", doc);

        _embeddings.Verify(e => e.EmbedAsync(It.IsAny<string>(), It.IsAny<CancellationToken>()),
            Times.Once);
    }

    [Test]
    public async Task IndexAsync_LongDocument_CallsEmbedOncePerChunk()
    {
        // Generate text long enough to produce multiple chunks (MaxChunkSize=100)
        var longText = string.Join(". ", Enumerable.Range(1, 30).Select(i => $"Sentence {i}"));
        var doc      = MakeDocument(longText);

        await _indexer.IndexAsync("collection", doc);

        var expectedChunks = new TextChunker(new ChunkingOptions { MaxChunkSize = 100, Overlap = 10 })
            .Chunk(longText).Count;

        _embeddings.Verify(e => e.EmbedAsync(It.IsAny<string>(), It.IsAny<CancellationToken>()),
            Times.Exactly(expectedChunks));
    }

    // ─── Vector store calls ──────────────────────────────────────

    [Test]
    public async Task IndexAsync_CallsUpsertWithCorrectCollectionName()
    {
        var doc = MakeDocument("text");

        await _indexer.IndexAsync("conduit_manual_documents", doc);

        _store.Verify(s => s.UpsertAsync(
            "conduit_manual_documents",
            It.IsAny<IReadOnlyList<PointStruct>>(),
            true,
            It.IsAny<CancellationToken>()),
            Times.Once);
    }

    // ─── Batch indexing ──────────────────────────────────────────

    [Test]
    public async Task IndexBatchAsync_IndexesEachDocumentSeparately()
    {
        var docs = new List<SourceDocument>
        {
            MakeDocument("first"),
            MakeDocument("second"),
            MakeDocument("third")
        };

        await _indexer.IndexBatchAsync("collection", docs);

        _store.Verify(s => s.UpsertAsync(
            It.IsAny<string>(),
            It.IsAny<IReadOnlyList<PointStruct>>(),
            It.IsAny<bool>(),
            It.IsAny<CancellationToken>()),
            Times.Exactly(3));
    }

    // ─── Deterministic ID ────────────────────────────────────────

    [Test]
    public async Task IndexAsync_SameDocumentId_ProducesSameQdrantPointId()
    {
        string? capturedId1 = null;
        string? capturedId2 = null;

        _store
            .Setup(s => s.UpsertAsync(
                It.IsAny<string>(),
                It.IsAny<IReadOnlyList<PointStruct>>(),
                It.IsAny<bool>(),
                It.IsAny<CancellationToken>()))
            .Callback<string, IReadOnlyList<PointStruct>, bool, CancellationToken>(
                (_, pts, _, _) =>
                {
                    if (capturedId1 is null) capturedId1 = pts[0].Id.Uuid;
                    else                     capturedId2 = pts[0].Id.Uuid;
                })
            .Returns(Task.CompletedTask);

        var doc = MakeDocument("text");
        await _indexer.IndexAsync("col", doc);
        await _indexer.IndexAsync("col", doc); // same document

        Assert.That(capturedId1, Is.EqualTo(capturedId2),
            "Re-indexing the same document should produce the same Qdrant point ID");
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private static SourceDocument MakeDocument(string text, string? id = null) =>
        new(
            Id:         id ?? "test-doc",
            Text:       text,
            Tags:       new Dictionary<string, string> { ["source_name"] = "test" },
            Properties: new Dictionary<string, string>()
        );
}
