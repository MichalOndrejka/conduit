from __future__ import annotations

from typing import Optional
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


class ManualDocumentSource(Source):
    def __init__(self, source: SourceDefinition) -> None:
        self._source = source

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        content = self._source.get_config(ConfigKeys.CONTENT)
        title = self._source.get_config(ConfigKeys.TITLE) or self._source.name

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Loading {title}"))

        return [SourceDocument(
            id=self._source.id,
            text=content,
            tags={"source_name": self._source.name},
            properties={"title": title},
        )]
