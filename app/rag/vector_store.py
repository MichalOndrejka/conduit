from __future__ import annotations

from typing import Any, Optional
from qdrant_client import AsyncQdrantClient
from qdrant_client.models import (
    Distance, VectorParams, PointStruct, Filter, FieldCondition,
    MatchValue, ScoredPoint, ScrollResult, PointIdsList, FilterSelector,
)
from app.config import AppConfig
from app.models import SearchResult, PayloadKeys


class VectorStore:
    def __init__(self, cfg: AppConfig) -> None:
        self._client = AsyncQdrantClient(
            host=cfg.qdrant.host,
            port=cfg.qdrant.port,
            timeout=30,
            check_compatibility=False,
        )
        self._dimensions = cfg.embedding.dimensions

    @property
    def client(self) -> AsyncQdrantClient:
        return self._client

    async def collection_exists(self, name: str) -> bool:
        try:
            collections = await self._client.get_collections()
            return any(c.name == name for c in collections.collections)
        except Exception:
            return False

    async def create_collection(self, name: str, dimensions: int | None = None) -> None:
        size = dimensions if dimensions is not None else self._dimensions
        await self._client.create_collection(
            collection_name=name,
            vectors_config=VectorParams(size=size, distance=Distance.COSINE),
        )

    async def delete_collection(self, name: str) -> None:
        await self._client.delete_collection(collection_name=name)

    async def upsert(self, collection: str, points: list[PointStruct]) -> None:
        await self._client.upsert(collection_name=collection, points=points)

    async def search(
        self,
        collection: str,
        vector: list[float],
        limit: int = 5,
        tags: Optional[dict[str, str]] = None,
    ) -> list[ScoredPoint]:
        query_filter = _build_filter(tags) if tags else None
        result = await self._client.query_points(
            collection_name=collection,
            query=vector,
            limit=limit,
            query_filter=query_filter,
            with_payload=True,
        )
        return result.points

    async def scroll(
        self,
        collection: str,
        scroll_filter: Optional[Filter] = None,
        limit: int = 20,
        offset: Optional[str] = None,
    ) -> tuple[list[Any], Optional[str]]:
        points, next_offset = await self._client.scroll(
            collection_name=collection,
            scroll_filter=scroll_filter,
            limit=limit,
            offset=offset,
            with_payload=True,
            with_vectors=False,
        )
        next_str = str(next_offset) if next_offset is not None else None
        return points, next_str

    async def delete_by_filter(self, collection: str, filter: Filter) -> None:
        """Delete all points matching the given filter."""
        await self._client.delete(
            collection_name=collection,
            points_selector=FilterSelector(filter=filter),
        )

    async def count_points(self, collection: str, filter: Optional[Filter] = None) -> int:
        try:
            result = await self._client.count(collection_name=collection, count_filter=filter, exact=False)
            return result.count
        except Exception:
            return 0

    async def health_check(self) -> None:
        """Raises if Qdrant is unreachable."""
        await self._client.get_collections()


def _build_filter(tags: dict[str, str]) -> Filter:
    conditions = [
        FieldCondition(key=PayloadKeys.tag(k), match=MatchValue(value=v))
        for k, v in tags.items()
    ]
    return Filter(must=conditions)


def point_to_search_result(point: ScoredPoint) -> SearchResult:
    payload = point.payload or {}
    text = payload.get(PayloadKeys.TEXT, "")
    score = float(point.score)

    tags: dict[str, str] = {}
    props: dict[str, str] = {}
    for k, v in payload.items():
        if k.startswith(PayloadKeys.TAG_PREFIX):
            tags[k[len(PayloadKeys.TAG_PREFIX):]] = str(v)
        elif k.startswith(PayloadKeys.PROP_PREFIX):
            props[k[len(PayloadKeys.PROP_PREFIX):]] = str(v)

    return SearchResult(
        id=str(point.id),
        score=score,
        text=text,
        tags=tags,
        properties=props,
    )
