from app.config import AppConfig, ChunkingConfig
from app.rag.chunker import TextChunker


def _chunker(max_size: int = 100, overlap: int = 10) -> TextChunker:
    return TextChunker(AppConfig(chunking=ChunkingConfig(max_chunk_size=max_size, overlap=overlap)))


# ── edge cases ────────────────────────────────────────────────────────────────

def test_empty_text_returns_no_chunks():
    assert _chunker().chunk("") == []


def test_short_text_returns_single_chunk():
    chunks = _chunker(max_size=200).chunk("Hello world.")
    assert len(chunks) == 1
    assert chunks[0].index == 0
    assert chunks[0].start_offset == 0


def test_single_chunk_contains_full_text():
    text = "Some short text."
    chunks = _chunker(max_size=200).chunk(text)
    assert chunks[0].text == text


# ── multi-chunk behaviour ─────────────────────────────────────────────────────

def test_long_text_produces_multiple_chunks():
    text = "This is a sentence. " * 20
    chunks = _chunker(max_size=50, overlap=5).chunk(text)
    assert len(chunks) > 1


def test_chunks_indexed_sequentially():
    text = "Word. " * 40
    chunks = _chunker(max_size=50, overlap=5).chunk(text)
    for i, c in enumerate(chunks):
        assert c.index == i


def test_no_chunk_exceeds_max_size():
    chunks = _chunker(max_size=50, overlap=5).chunk("a" * 500)
    for c in chunks:
        assert len(c.text) <= 50


def test_start_offsets_are_monotonically_non_decreasing():
    text = "Alpha. Beta. Gamma. Delta. Epsilon. Zeta. Eta. Theta. Iota. Kappa."
    chunks = _chunker(max_size=30, overlap=5).chunk(text)
    for i in range(1, len(chunks)):
        assert chunks[i].start_offset >= chunks[i - 1].start_offset


def test_newline_used_as_break_when_no_sentence_boundary():
    # Lines of 30 chars with no sentence punctuation — chunker must break at \n
    line = "x" * 30
    text = ("\n".join([line] * 10))
    chunks = _chunker(max_size=80, overlap=0).chunk(text)
    assert len(chunks) > 1
    for c in chunks:
        assert len(c.text) <= 80


def test_overlap_causes_next_chunk_to_start_before_end_of_prior():
    text = "First sentence ends here. Second one follows on. Third is the last one here."
    chunks = _chunker(max_size=40, overlap=15).chunk(text)
    if len(chunks) >= 2:
        assert chunks[1].start_offset < chunks[0].end_offset
