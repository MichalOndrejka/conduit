from __future__ import annotations

import asyncio
import json
import logging
from pathlib import Path

from app.config import AppConfig
from app.models import CollectionNames
from app.rag.vector_store import VectorStore

logger = logging.getLogger(__name__)

_FINGERPRINT_PATH = Path("conduit-embedding.json")


class QdrantHealth:
    def __init__(self) -> None:
        self.is_ready = False
        self.pending = True
        self.error: str | None = None
        self.retry_attempt: int = 0
        self.max_retries: int = 30


class EmbeddingHealth:
    def __init__(self) -> None:
        self.is_ready = False
        self.pending = True
        self.error: str | None = None


class LlmHealth:
    def __init__(self) -> None:
        self.is_ready = False
        self.pending = True
        self.error: str | None = None


async def _probe_embedding(embedding, embedding_health: EmbeddingHealth, store) -> None:
    try:
        probe = await embedding.embed("probe")
        actual_dims = len(probe)
        if actual_dims != store._dimensions:
            store._dimensions = actual_dims
        embedding_health.is_ready = True
        embedding_health.error = None
        logger.info("Embedding model recovered")
    except Exception as exc:
        embedding_health.is_ready = False
        embedding_health.error = str(exc)
        logger.warning("Embedding model unreachable: %s", exc)


async def _probe_llm(cfg: AppConfig, llm_health: LlmHealth) -> None:
    if not cfg.preprocessing.enabled or not cfg.preprocessing.model:
        llm_health.is_ready = True
        llm_health.error = None
        return
    try:
        import httpx
        from openai import AsyncOpenAI
        client = AsyncOpenAI(base_url=cfg.preprocessing.base_url or "http://localhost:11434/v1", api_key="ollama", http_client=httpx.AsyncClient())
        await client.chat.completions.create(
            model=cfg.preprocessing.model,
            messages=[{"role": "user", "content": "hi"}],
            max_tokens=1,
        )
        llm_health.is_ready = True
        llm_health.error = None
        logger.info("Preprocessing LLM recovered")
    except Exception as exc:
        llm_health.is_ready = False
        llm_health.error = str(exc)


async def retry_failed_probes(cfg: AppConfig, store: VectorStore, qdrant_health: QdrantHealth, embedding=None, embedding_health: EmbeddingHealth | None = None, llm_health: LlmHealth | None = None, config_store=None) -> None:
    """Re-probe services that failed at startup every 30 s until all recover."""
    while True:
        await asyncio.sleep(30)
        if (qdrant_health.is_ready
                and (embedding_health is None or embedding_health.is_ready)
                and (llm_health is None or llm_health.is_ready)):
            return

        if embedding_health is not None and not embedding_health.is_ready and embedding is not None:
            await _probe_embedding(embedding, embedding_health, store)

        if llm_health is not None and not llm_health.is_ready:
            await _probe_llm(cfg, llm_health)

        if not qdrant_health.is_ready:
            try:
                await store.health_check()
                logger.info("Qdrant recovered — re-running setup")
                await bootstrap_qdrant(cfg, store, qdrant_health, config_store, embedding,
                                       embedding_health, llm_health)
                return  # bootstrap_qdrant handles the rest
            except Exception as exc:
                qdrant_health.error = str(exc)


async def bootstrap_qdrant(cfg: AppConfig, store: VectorStore, health: QdrantHealth, config_store=None, embedding=None, embedding_health: EmbeddingHealth | None = None, llm_health: LlmHealth | None = None) -> None:
    """Verify Qdrant connectivity, detect embedding model changes, create collections."""

    # Probe embedding and LLM first — they are independent of Qdrant.
    if embedding is not None and embedding_health is not None:
        await _probe_embedding(embedding, embedding_health, store)
        embedding_health.pending = False

    if llm_health is not None:
        await _probe_llm(cfg, llm_health)
        llm_health.pending = False

    max_retries = 30
    health.max_retries = max_retries
    for attempt in range(1, max_retries + 1):
        health.retry_attempt = attempt
        try:
            await store.health_check()
            break
        except Exception as exc:
            if attempt == max_retries:
                health.is_ready = False
                health.pending = False
                health.error = str(exc)
                logger.error("Qdrant not available after %d retries: %s", max_retries, exc)
                return
            logger.warning("Qdrant not ready (attempt %d/%d): %s", attempt, max_retries, exc)
            await asyncio.sleep(2)

    ec = cfg.embedding
    fingerprint = {
        "model": ec.model,
        "base_url": ec.base_url,
        "dimensions": ec.dimensions,
    }

    if _FINGERPRINT_PATH.exists():
        saved = json.loads(_FINGERPRINT_PATH.read_text())
        if saved != fingerprint:
            logger.info("Embedding config changed — dropping all collections for re-index")
            for name in CollectionNames.ALL:
                try:
                    if await store.collection_exists(name):
                        await store.delete_collection(name)
                except Exception as exc:
                    logger.warning("Failed to drop collection %s: %s", name, exc)

    _FINGERPRINT_PATH.write_text(json.dumps(fingerprint, indent=2))

    # Ensure all collections exist
    for name in CollectionNames.ALL:
        if not await store.collection_exists(name):
            await store.create_collection(name)
            logger.info("Created collection: %s", name)

    # For each source marked completed, verify it actually has data in Qdrant
    if config_store is not None:
        from qdrant_client.models import FieldCondition, Filter, MatchValue
        from app.models import PayloadKeys
        from app.sources.factory import collection_for

        sources = await config_store.get_all()
        stale = []
        for source in sources:
            if source.sync_status != "completed":
                continue
            collection = collection_for(source)
            tag_key = PayloadKeys.tag("source_id")
            f = Filter(must=[FieldCondition(key=tag_key, match=MatchValue(value=source.id))])
            count = await store.count_points(collection, f)
            if count == 0:
                source.sync_status = "needs-reindex"
                stale.append(source)

        for source in stale:
            await config_store.save(source)
            logger.info("Source '%s' has no data in Qdrant — marked for re-sync", source.name)

    health.is_ready = True
    health.pending = False
    health.error = None
    logger.info("Qdrant ready")
