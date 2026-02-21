using NUnit.Framework;
using Qdrant.Demo.Api.Models;
using Qdrant.Demo.Api.Services;

namespace Qdrant.Demo.Api.Tests;

[TestFixture]
public class TextChunkerTests
{
    // ─── Short text (single chunk) ──────────────────────────

    [Test]
    public void ShortText_ReturnsSingleChunk()
    {
        var chunker = CreateChunker(maxSize: 100, overlap: 20);

        var chunks = chunker.Chunk("Hello, world!");

        Assert.That(chunks, Has.Count.EqualTo(1));
        Assert.That(chunks[0].Text, Is.EqualTo("Hello, world!"));
        Assert.That(chunks[0].Index, Is.EqualTo(0));
        Assert.That(chunks[0].StartOffset, Is.EqualTo(0));
        Assert.That(chunks[0].EndOffset, Is.EqualTo(13));
    }

    [Test]
    public void TextExactlyAtLimit_ReturnsSingleChunk()
    {
        var text = new string('a', 100);
        var chunker = CreateChunker(maxSize: 100, overlap: 20);

        var chunks = chunker.Chunk(text);

        Assert.That(chunks, Has.Count.EqualTo(1));
        Assert.That(chunks[0].Text, Is.EqualTo(text));
    }

    // ─── Multi-chunk splitting ──────────────────────────────

    [Test]
    public void LongText_IsSplitIntoMultipleChunks()
    {
        // 250 chars, max 100 per chunk → at least 3 chunks
        var text = string.Join(". ", Enumerable.Range(1, 25)
            .Select(i => $"Sentence {i}"));
        var chunker = CreateChunker(maxSize: 100, overlap: 20);

        var chunks = chunker.Chunk(text);

        Assert.That(chunks.Count, Is.GreaterThanOrEqualTo(3));
    }

    [Test]
    public void AllChunks_HaveSequentialIndices()
    {
        var text = new string('x', 500);
        var chunker = CreateChunker(maxSize: 100, overlap: 10);

        var chunks = chunker.Chunk(text);

        for (var i = 0; i < chunks.Count; i++)
            Assert.That(chunks[i].Index, Is.EqualTo(i));
    }

    [Test]
    public void NoChunk_ExceedsMaxSize()
    {
        var text = string.Join(". ", Enumerable.Range(1, 50)
            .Select(i => $"Sentence number {i} with some content"));
        var chunker = CreateChunker(maxSize: 100, overlap: 20);

        var chunks = chunker.Chunk(text);

        foreach (var chunk in chunks)
            Assert.That(chunk.Text.Length, Is.LessThanOrEqualTo(100),
                $"Chunk {chunk.Index} is {chunk.Text.Length} chars");
    }

    // ─── Overlap ────────────────────────────────────────────

    [Test]
    public void ConsecutiveChunks_HaveOverlap()
    {
        // Use a long string without sentence boundaries to force
        // overlapping at character level.
        var text = string.Concat(Enumerable.Range(0, 300).Select(i => (char)('a' + i % 26)));
        var chunker = CreateChunker(maxSize: 100, overlap: 20);

        var chunks = chunker.Chunk(text);

        Assert.That(chunks.Count, Is.GreaterThanOrEqualTo(2));

        // The end of chunk[0] should overlap with the start of chunk[1]
        var tail0 = chunks[0].Text[^20..];
        Assert.That(chunks[1].Text, Does.StartWith(tail0),
            "Consecutive chunks should share 'overlap' characters at their boundary");
    }

    [Test]
    public void ZeroOverlap_ProducesNoOverlap()
    {
        var text = new string('x', 250);
        var chunker = CreateChunker(maxSize: 100, overlap: 0);

        var chunks = chunker.Chunk(text);

        // With 250 chars, max 100, overlap 0 → 3 chunks (100+100+50)
        Assert.That(chunks.Count, Is.EqualTo(3));
    }

    // ─── Sentence-boundary awareness ────────────────────────

    [Test]
    public void PrefersSentenceBoundary_OverHardCut()
    {
        // 120 chars with clear sentence boundary around char 80
        var text = "First sentence is here. " +   // 24 chars
                   "Second sentence follows. " +   // 25 chars → 49
                   "Third sentence comes next. " + // 27 chars → 76
                   "Fourth sentence is longer and extends beyond the limit to force a split."; // +72 → 148

        var chunker = CreateChunker(maxSize: 100, overlap: 10);
        var chunks = chunker.Chunk(text);

        // First chunk should end at a sentence boundary, not mid-word
        Assert.That(chunks[0].Text, Does.EndWith("."),
            "Chunk should end at sentence boundary (period)");
    }

    [Test]
    public void ParagraphBreak_IsPreferredBoundary()
    {
        var text = "First paragraph content here.\n" +
                   "Second paragraph starts here and continues with more text that pushes " +
                   "beyond the limit so it must be split.";

        var chunker = CreateChunker(maxSize: 80, overlap: 10);
        var chunks = chunker.Chunk(text);

        Assert.That(chunks.Count, Is.GreaterThan(1));
        // First chunk should break at or before the newline (30 chars in),
        // not at the hard 80-char limit.
        Assert.That(chunks[0].Text.Length, Is.LessThan(80),
            "Chunk should break at paragraph boundary before hitting max size");
    }

    // ─── Edge cases ─────────────────────────────────────────

    [Test]
    public void ThrowsOnNull()
    {
        var chunker = CreateChunker();
        Assert.Throws<ArgumentNullException>(() => chunker.Chunk(null!));
    }

    [Test]
    public void ThrowsOnEmpty()
    {
        var chunker = CreateChunker();
        Assert.Throws<ArgumentException>(() => chunker.Chunk(""));
    }

    [Test]
    public void ThrowsOnWhitespace()
    {
        var chunker = CreateChunker();
        Assert.Throws<ArgumentException>(() => chunker.Chunk("   "));
    }

    [Test]
    public void SingleCharacterText_ReturnsSingleChunk()
    {
        var chunker = CreateChunker(maxSize: 100, overlap: 20);
        var chunks = chunker.Chunk("X");

        Assert.That(chunks, Has.Count.EqualTo(1));
        Assert.That(chunks[0].Text, Is.EqualTo("X"));
    }

    [Test]
    public void VeryLargeOverlap_DoesNotCauseInfiniteLoop()
    {
        // Overlap larger than chunk size — the guard should prevent infinite loops
        var text = new string('a', 200);
        var chunker = CreateChunker(maxSize: 50, overlap: 60);

        // Should complete without hanging
        var chunks = chunker.Chunk(text);

        Assert.That(chunks.Count, Is.GreaterThan(1));
    }

    // ─── Default options ────────────────────────────────────

    [Test]
    public void DefaultOptions_MaxChunkSize_Is2000()
    {
        var options = new ChunkingOptions();
        Assert.That(options.MaxChunkSize, Is.EqualTo(2000));
    }

    [Test]
    public void DefaultOptions_Overlap_Is200()
    {
        var options = new ChunkingOptions();
        Assert.That(options.Overlap, Is.EqualTo(200));
    }

    [Test]
    public void DefaultOptions_TextUnder2000_IsNotChunked()
    {
        var text = new string('a', 1999);
        var chunker = CreateChunker(); // defaults: 2000/200

        var chunks = chunker.Chunk(text);
        Assert.That(chunks, Has.Count.EqualTo(1));
    }

    [Test]
    public void DefaultOptions_TextOver2000_IsChunked()
    {
        var text = string.Join(". ", Enumerable.Range(1, 200)
            .Select(i => $"Sentence number {i}"));
        var chunker = CreateChunker(); // defaults: 2000/200

        Assert.That(text.Length, Is.GreaterThan(2000), "Precondition: text should exceed 2000 chars");

        var chunks = chunker.Chunk(text);
        Assert.That(chunks.Count, Is.GreaterThan(1));
    }

    // ─── Helpers ────────────────────────────────────────────

    private static TextChunker CreateChunker(int maxSize = 2000, int overlap = 200) =>
        new(new ChunkingOptions { MaxChunkSize = maxSize, Overlap = overlap });
}
