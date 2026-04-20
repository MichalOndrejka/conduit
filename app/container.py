from __future__ import annotations

"""Global service container — populated during FastAPI lifespan, referenced by routes."""

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from app.memory.service import MemoryService
    from app.rag.bootstrap import QdrantHealth
    from app.rag.preprocessor import DocumentPreprocessor
    from app.rag.search import SearchService
    from app.rag.vector_store import VectorStore
    from app.store.source_config import SourceConfigStore
    from app.store.sync_progress import SyncProgressStore
    from app.sync.service import SyncService

health: "QdrantHealth"
config_store: "SourceConfigStore"
sync_service: "SyncService"
progress_store: "SyncProgressStore"
vector_store: "VectorStore"
search_service: "SearchService"
memory_service: "MemoryService"
preprocessor: "DocumentPreprocessor"
