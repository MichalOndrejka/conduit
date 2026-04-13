from unittest.mock import AsyncMock, MagicMock

import pytest

from app.config import AppConfig, ChunkingConfig
from app.models import PayloadKeys, SourceDocument
from app.rag.chunker import TextChunker
from app.rag.indexer import DocumentIndexer, _make_chunk_id, _make_id


# ── Helpers ────────────────────────────────────────────────────────────────────

def _make_store(upsert_side_effect=None):
    store = MagicMock()
    store.collection_exists = AsyncMock(return_value=True)
    store.create_collection = AsyncMock()
    store.upsert = AsyncMock(side_effect=upsert_side_effect)
    store.client = MagicMock()
    store.client.delete = AsyncMock()
    return store


def _make_embedding(dim: int = 4):
    svc = MagicMock()
    svc.embed = AsyncMock(return_value=[0.1] * dim)
    return svc


def _chunker(max_size: int = 500) -> TextChunker:
    return TextChunker(AppConfig(chunking=ChunkingConfig(max_chunk_size=max_size, overlap=0)))


def _doc(id_: str = "doc1", text: str = "short text") -> SourceDocument:
    return SourceDocument(id=id_, text=text, tags={"source_id": "s1"}, properties={"title": "T"})


# ── ID helper unit tests ───────────────────────────────────────────────────────

def test_make_id_is_deterministic():
    assert _make_id("abc") == _make_id("abc")


def test_make_chunk_id_differs_by_index():
    assert _make_chunk_id("doc", 0) != _make_chunk_id("doc", 1)


def test_make_chunk_id_is_deterministic():
    assert _make_chunk_id("doc", 2) == _make_chunk_id("doc", 2)


# ── index_batch basic ──────────────────────────────────────────────────────────

async def test_upsert_called_once_for_single_doc():
    store = _make_store()
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [_doc()])
    store.upsert.assert_called_once()


async def test_upsert_not_called_for_empty_doc_list():
    store = _make_store()
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [])
    store.upsert.assert_not_called()


async def test_single_chunk_doc_uses_make_id():
    store = _make_store()
    doc = _doc(id_="d1", text="short")
    await DocumentIndexer(store, _make_embedding(), _chunker(max_size=1000)).index_batch("col", [doc])
    point = store.upsert.call_args[0][1][0]
    assert point.id == _make_id("d1")


async def test_multi_chunk_doc_uses_chunk_ids():
    store = _make_store()
    doc = _doc(id_="d1", text="word1 word2 word3 word4 word5 word6 word7 word8 word9 word10")
    await DocumentIndexer(store, _make_embedding(), _chunker(max_size=10)).index_batch("col", [doc])
    points = store.upsert.call_args[0][1]
    assert len(points) >= 2
    assert points[0].id == _make_chunk_id("d1", 0)


async def test_creates_collection_when_not_exists():
    store = _make_store()
    store.collection_exists = AsyncMock(return_value=False)
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("new-col", [_doc()])
    store.create_collection.assert_called_once_with("new-col", dimensions=4)


async def test_does_not_create_collection_when_exists():
    store = _make_store()
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [_doc()])
    store.create_collection.assert_not_called()


# ── Payload content ────────────────────────────────────────────────────────────

async def test_payload_contains_text_key():
    store = _make_store()
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [_doc(text="hello")])
    payload = store.upsert.call_args[0][1][0].payload
    assert PayloadKeys.TEXT in payload


async def test_payload_indexed_at_ms_is_positive_int():
    store = _make_store()
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [_doc()])
    payload = store.upsert.call_args[0][1][0].payload
    assert isinstance(payload[PayloadKeys.INDEXED_AT_MS], int)
    assert payload[PayloadKeys.INDEXED_AT_MS] > 0


async def test_payload_tags_are_prefixed():
    store = _make_store()
    doc = SourceDocument(id="d", text="x", tags={"source_id": "s1"}, properties={})
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [doc])
    payload = store.upsert.call_args[0][1][0].payload
    assert payload.get("tag_source_id") == "s1"


async def test_payload_properties_are_prefixed():
    store = _make_store()
    doc = SourceDocument(id="d", text="x", tags={}, properties={"title": "My Title"})
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [doc])
    payload = store.upsert.call_args[0][1][0].payload
    assert payload.get("prop_title") == "My Title"


async def test_payload_source_doc_id_matches_document_id():
    store = _make_store()
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [_doc(id_="doc-xyz")])
    payload = store.upsert.call_args[0][1][0].payload
    assert payload[PayloadKeys.SOURCE_DOC_ID] == "doc-xyz"


# ── Progress callback ──────────────────────────────────────────────────────────

async def test_progress_callback_called_with_current_and_total():
    store = _make_store()
    calls: list[tuple[int, int]] = []
    await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch(
        "col", [_doc("d1"), _doc("d2")], progress_cb=lambda c, t: calls.append((c, t))
    )
    assert calls == [(1, 2), (2, 2)]


# ── Rollback on partial failure ────────────────────────────────────────────────

async def test_rollback_triggered_after_first_batch_succeeds():
    """100 points written, second batch of 50 fails — rollback deletes the 100."""
    call_count = 0

    async def failing_upsert(collection, points):
        nonlocal call_count
        call_count += 1
        if call_count == 2:
            raise RuntimeError("Qdrant down")

    store = _make_store(upsert_side_effect=failing_upsert)
    docs = [_doc(id_=f"d{i}", text=f"word{i}") for i in range(150)]

    with pytest.raises(RuntimeError, match="Qdrant down"):
        await DocumentIndexer(store, _make_embedding(), _chunker(max_size=1000)).index_batch("col", docs)

    store.client.delete.assert_called_once()


async def test_no_rollback_when_first_batch_fails():
    """Nothing was written so delete must not be called."""
    store = _make_store(upsert_side_effect=RuntimeError("immediate fail"))

    with pytest.raises(RuntimeError):
        await DocumentIndexer(store, _make_embedding(), _chunker()).index_batch("col", [_doc()])

    store.client.delete.assert_not_called()
