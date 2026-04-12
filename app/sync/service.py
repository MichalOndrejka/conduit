from __future__ import annotations

import logging
from datetime import datetime, timezone

from app.models import SyncProgress
from app.rag.indexer import DocumentIndexer
from app.sources.factory import SourceFactory, collection_for
from app.store.source_config import SourceConfigStore
from app.store.sync_progress import SyncProgressStore

logger = logging.getLogger(__name__)


class SyncService:
    def __init__(
        self,
        config_store: SourceConfigStore,
        source_factory: SourceFactory,
        indexer: DocumentIndexer,
        progress_store: SyncProgressStore,
    ) -> None:
        self._config_store = config_store
        self._factory = source_factory
        self._indexer = indexer
        self._progress_store = progress_store

    async def sync(self, source_id: str) -> None:
        source = await self._config_store.get_by_id(source_id)
        if source is None:
            logger.error("Source not found: %s", source_id)
            return

        source.sync_status = "syncing"
        source.sync_error = None
        await self._config_store.save(source)

        self._progress_store.set(source_id, SyncProgress(phase="fetching"))

        impl = self._factory.create(source)
        collection = collection_for(source)

        # ── Phase 1: fetch ────────────────────────────────────────────────────
        try:
            def on_fetch_progress(p: SyncProgress) -> None:
                self._progress_store.set(source_id, p)

            docs = await impl.fetch_documents(progress_cb=on_fetch_progress)
        except Exception as exc:
            logger.exception("Fetch failed for source %s", source_id)
            source.sync_status = "failed"
            source.sync_error = str(exc)
            source.sync_error_phase = "fetch"
            self._progress_store.clear(source_id)
            await self._config_store.save(source)
            return

        logger.info("Indexing %d documents for source %s", len(docs), source_id)
        self._progress_store.set(source_id, SyncProgress(phase="indexing", total=len(docs)))

        # ── Phase 2: embed & index ────────────────────────────────────────────
        try:
            def on_embed_progress(current: int, total: int) -> None:
                self._progress_store.set(source_id, SyncProgress(
                    phase="indexing",
                    current=current,
                    total=total,
                ))

            await self._indexer.index_batch(collection, docs, progress_cb=on_embed_progress)

            source.sync_status = "completed"
            source.last_synced_at = datetime.now(timezone.utc)
            source.sync_error = None
            source.sync_error_phase = None
        except Exception as exc:
            logger.exception("Embed/index failed for source %s", source_id)
            source.sync_status = "failed"
            source.sync_error = str(exc)
            source.sync_error_phase = "embed"
        finally:
            self._progress_store.clear(source_id)
            await self._config_store.save(source)

    async def sync_all(self) -> None:
        sources = await self._config_store.get_all()
        for source in sources:
            await self.sync(source.id)
