from __future__ import annotations

from typing import Optional

from app.models import SearchResult
from app.rag.embedding import EmbeddingService
from app.rag.vector_store import VectorStore, point_to_search_result


class SearchService:
    def __init__(self, vector_store: VectorStore, embedding: EmbeddingService) -> None:
        self._store = vector_store
        self._embedding = embedding

    async def search(
        self,
        collection: str,
        query: str,
        top_k: int = 5,
        tags: Optional[dict[str, str]] = None,
    ) -> list[SearchResult]:
        vector = await self._embedding.embed(query)
        points = await self._store.search(collection, vector, top_k, tags)
        return [point_to_search_result(p) for p in points]
