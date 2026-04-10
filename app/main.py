from __future__ import annotations

import asyncio
import logging
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI
from mcp.server.fastmcp import FastMCP

from app import container
from app.ado.client import AdoClient
from app.config import load_config
from app.memory.service import MemoryService
from app.mcp_tools.tools import register_tools
from app.parsing.registry import ParserRegistry
from app.rag.bootstrap import QdrantHealth, bootstrap_qdrant
from app.rag.chunker import TextChunker
from app.rag.embedding import EmbeddingService
from app.rag.indexer import DocumentIndexer
from app.rag.search import SearchService
from app.rag.vector_store import VectorStore
from app.sources.factory import SourceFactory
from app.store.source_config import SourceConfigStore
from app.store.sync_progress import SyncProgressStore
from app.sync.service import SyncService
from app.templates_cfg import templates  # noqa: F401 — re-exported for convenience

logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s")

mcp = FastMCP("Conduit")


@asynccontextmanager
async def lifespan(app: FastAPI):
    cfg = load_config()

    embedding = EmbeddingService(cfg)
    chunker = TextChunker(cfg)
    vector_store = VectorStore(cfg)
    indexer = DocumentIndexer(vector_store, embedding, chunker)
    search_service = SearchService(vector_store, embedding)
    config_store = SourceConfigStore(cfg.sources_file_path)
    progress_store = SyncProgressStore()
    ado_client = AdoClient()
    parser_registry = ParserRegistry()
    source_factory = SourceFactory(ado_client, parser_registry)
    sync_service = SyncService(config_store, source_factory, indexer, progress_store)
    health = QdrantHealth()
    memory_service = MemoryService(vector_store, embedding)

    # Populate global container
    container.health = health
    container.config_store = config_store
    container.sync_service = sync_service
    container.progress_store = progress_store
    container.vector_store = vector_store
    container.search_service = search_service
    container.memory_service = memory_service

    register_tools(mcp, search_service, memory_service)

    # Bootstrap Qdrant in the background so the UI is immediately available
    # even when Qdrant is offline or still starting up.
    asyncio.create_task(bootstrap_qdrant(cfg, vector_store, health, config_store))

    # Run the FastMCP session manager so its task group is initialized.
    # streamable_http_app() is called at mount time (below), which lazily
    # creates the session manager — we just need to start it here.
    async with mcp.session_manager.run():
        yield


app = FastAPI(title="Conduit", lifespan=lifespan)

from app.web.routes import router  # noqa: E402 — imported after app creation to avoid circular
app.include_router(router)
app.mount("/", mcp.streamable_http_app())


def run():
    import uvicorn
    uvicorn.run("app.main:app", host="0.0.0.0", port=8000, reload=False)


if __name__ == "__main__":
    run()
