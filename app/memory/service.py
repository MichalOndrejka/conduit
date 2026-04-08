from __future__ import annotations

import logging
import uuid
from datetime import datetime, timezone
from typing import Optional

from qdrant_client.models import PointIdsList, PointStruct

log = logging.getLogger(__name__)

from app.models import CollectionNames, PayloadKeys
from app.rag.embedding import EmbeddingService
from app.rag.vector_store import VectorStore


class MemoryService:
    """Stores and retrieves LLM experience entries in Qdrant."""

    COLLECTION = CollectionNames.EXPERIENCE

    def __init__(self, vector_store: VectorStore, embedding: EmbeddingService) -> None:
        self._store = vector_store
        self._embedding = embedding

    # ── Write ──────────────────────────────────────────────────────────────────

    async def remember(self, situation: str, guidance: str) -> str:
        """Embed situation and store situation+guidance. Returns the entry UUID."""
        entry_id = str(uuid.uuid4())
        vector = await self._embedding.embed(situation)
        now = datetime.now(tz=timezone.utc)

        point = PointStruct(
            id=entry_id,
            vector=vector,
            payload={
                PayloadKeys.TEXT: situation,
                PayloadKeys.prop("guidance"): guidance,
                PayloadKeys.INDEXED_AT_MS: int(now.timestamp() * 1000),
                PayloadKeys.prop("created_at"): now.isoformat(),
            },
        )
        try:
            await self._store.upsert(self.COLLECTION, [point])
        except Exception:
            log.warning("Upsert failed for experience %s — rolling back", entry_id)
            try:
                await self._store.client.delete(
                    collection_name=self.COLLECTION,
                    points_selector=PointIdsList(points=[entry_id]),
                )
            except Exception:
                pass
            raise
        return entry_id

    # ── Read ───────────────────────────────────────────────────────────────────

    async def retrieve(self, query: str, top_k: int = 5) -> list[dict]:
        """Semantic search over situations; returns matching guidance entries."""
        vector = await self._embedding.embed(query)
        scored_points = await self._store.search(self.COLLECTION, vector, limit=top_k)
        results = []
        for p in scored_points:
            payload = p.payload or {}
            results.append({
                "situation": payload.get(PayloadKeys.TEXT, ""),
                "guidance": payload.get(PayloadKeys.prop("guidance"), ""),
                "score": round(float(p.score), 3),
            })
        return results

    async def get_all_paginated(
        self,
        limit: int = 20,
        offset: Optional[str] = None,
    ) -> tuple[list[dict], Optional[str]]:
        """Paginated list of experience entries for the UI, newest first."""
        points, next_offset = await self._store.scroll(
            self.COLLECTION, limit=limit, offset=offset
        )
        entries = [_point_to_dict(p) for p in points]
        entries.sort(key=lambda e: e["created_at"], reverse=True)
        return entries, next_offset

    async def get_all_with_vectors(self) -> list[dict]:
        """Return all entries with their raw embedding vectors (for PCA map)."""
        result = []
        offset = None
        while True:
            points, next_page = await self._store.client.scroll(
                collection_name=self.COLLECTION,
                limit=500,
                offset=offset,
                with_payload=True,
                with_vectors=True,
            )
            for p in points:
                payload = p.payload or {}
                vector = p.vector
                if vector is None:
                    continue
                if isinstance(vector, dict):
                    vector = next(iter(vector.values()), None)
                    if not vector:
                        continue
                result.append({
                    "id": str(p.id),
                    "text": payload.get(PayloadKeys.TEXT, ""),
                    "guidance": payload.get(PayloadKeys.prop("guidance"), ""),
                    "created_at": payload.get(PayloadKeys.prop("created_at"), ""),
                    "vector": list(vector),
                })
            if next_page is None:
                break
            offset = next_page
        return result

    # ── Delete ─────────────────────────────────────────────────────────────────

    async def delete(self, entry_id: str) -> None:
        """Remove a single experience entry by ID."""
        from qdrant_client.models import PointIdsList
        await self._store.client.delete(
            collection_name=self.COLLECTION,
            points_selector=PointIdsList(points=[entry_id]),
        )

    async def count(self) -> int:
        """Return total number of stored experience entries."""
        try:
            info = await self._store.client.get_collection(self.COLLECTION)
            return info.points_count or 0
        except Exception:
            return 0


def _point_to_dict(point) -> dict:
    payload = point.payload or {}
    return {
        "id": str(point.id),
        "situation": payload.get(PayloadKeys.TEXT, ""),
        "guidance": payload.get(PayloadKeys.prop("guidance"), ""),
        "created_at": payload.get(PayloadKeys.prop("created_at"), ""),
    }
