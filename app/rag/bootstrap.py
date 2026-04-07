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
        self.error: str | None = None


async def bootstrap_qdrant(cfg: AppConfig, store: VectorStore, health: QdrantHealth) -> None:
    """Verify Qdrant connectivity, detect embedding model changes, create collections."""
    max_retries = 30

    for attempt in range(1, max_retries + 1):
        try:
            await store.health_check()
            break
        except Exception as exc:
            if attempt == max_retries:
                health.is_ready = False
                health.error = str(exc)
                logger.error("Qdrant not available after %d retries: %s", max_retries, exc)
                return
            logger.warning("Qdrant not ready (attempt %d/%d): %s", attempt, max_retries, exc)
            await asyncio.sleep(2)

    ec = cfg.embedding
    fingerprint = {
        "provider": ec.provider,
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

    health.is_ready = True
    health.error = None
    logger.info("Qdrant ready")
