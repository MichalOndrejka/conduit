from __future__ import annotations

import logging
import time
import uuid
from typing import Callable, Optional

from qdrant_client.models import FieldCondition, Filter, MatchValue, PointIdsList, PointStruct

from app.models import SourceDocument, PayloadKeys
from app.rag.chunker import TextChunker
from app.rag.embedding import EmbeddingService
from app.rag.vector_store import VectorStore

log = logging.getLogger(__name__)

_NAMESPACE = uuid.UUID("6ba7b810-9dad-11d1-80b4-00c04fd430c8")


def _make_id(doc_id: str) -> str:
    return str(uuid.uuid5(_NAMESPACE, doc_id))


def _make_chunk_id(doc_id: str, chunk_index: int) -> str:
    return str(uuid.uuid5(_NAMESPACE, f"{doc_id}_chunk_{chunk_index}"))


class DocumentIndexer:
    def __init__(
        self,
        vector_store: VectorStore,
        embedding: EmbeddingService,
        chunker: TextChunker,
    ) -> None:
        self._store = vector_store
        self._embedding = embedding
        self._chunker = chunker

    async def index(self, collection: str, doc: SourceDocument) -> None:
        await self.index_batch(collection, [doc])

    async def index_batch(
        self,
        collection: str,
        docs: list[SourceDocument],
        progress_cb: Optional[Callable[[int, int], None]] = None,
        replace_source_id: Optional[str] = None,
    ) -> None:
        now_ms = int(time.time() * 1000)

        # ── Phase 1: embed everything ──────────────────────────────────────────
        # Collection creation is deferred until after the first embed so we can
        # use the model's *actual* output dimension rather than the configured
        # one (e.g. Ollama models have a fixed output size the API cannot override).
        # No Qdrant writes happen here. If any embed call fails the exception
        # propagates immediately and nothing has been written, so no cleanup
        # is needed.
        points: list[PointStruct] = []

        for i, doc in enumerate(docs):
            chunks = self._chunker.chunk(doc.text)
            total_chunks = len(chunks)

            for chunk in chunks:
                vector = await self._embedding.embed(chunk.text)
                point_id = _make_id(doc.id) if total_chunks == 1 else _make_chunk_id(doc.id, chunk.index)

                payload: dict = {
                    PayloadKeys.TEXT: chunk.text,
                    PayloadKeys.INDEXED_AT_MS: now_ms,
                    PayloadKeys.SOURCE_DOC_ID: doc.id,
                    PayloadKeys.CHUNK_INDEX: str(chunk.index),
                    PayloadKeys.TOTAL_CHUNKS: str(total_chunks),
                }
                for k, v in doc.tags.items():
                    payload[PayloadKeys.tag(k)] = v
                for k, v in doc.properties.items():
                    payload[PayloadKeys.prop(k)] = v

                points.append(PointStruct(id=point_id, vector=vector, payload=payload))

            if progress_cb:
                progress_cb(i + 1, len(docs))

        if not points:
            return

        # Create the collection now that we know the actual vector size.
        if not await self._store.collection_exists(collection):
            actual_dims = len(points[0].vector)
            await self._store.create_collection(collection, dimensions=actual_dims)

        # ── Between phases: replace old vectors for this source ───────────────
        # Deleting here (after all embeds succeed, before any writes) means:
        # - embed failure → old vectors intact, nothing lost
        # - write failure → rollback removes the partial new batch
        if replace_source_id:
            filt = Filter(must=[
                FieldCondition(
                    key=PayloadKeys.tag("source_id"),
                    match=MatchValue(value=replace_source_id),
                )
            ])
            await self._store.delete_by_filter(collection, filt)

        # ── Phase 2: write to Qdrant with rollback on partial failure ──────────
        # Track every ID that was successfully committed so we can delete them
        # if a later batch throws.
        written_ids: list[str] = []
        try:
            batch_size = 100
            for i in range(0, len(points), batch_size):
                batch = points[i : i + batch_size]
                await self._store.upsert(collection, batch)
                written_ids.extend(str(p.id) for p in batch)
        except Exception:
            if written_ids:
                log.warning(
                    "Upsert failed after writing %d points — rolling back to Qdrant",
                    len(written_ids),
                )
                try:
                    await self._store.client.delete(
                        collection_name=collection,
                        points_selector=PointIdsList(points=written_ids),
                    )
                except Exception:
                    log.exception("Rollback delete also failed — collection %s may have orphaned points", collection)
            raise
