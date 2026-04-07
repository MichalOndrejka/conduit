from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Callable, Optional
from app.models import SourceDocument, SyncProgress


ProgressCallback = Callable[[SyncProgress], None]


class Source(ABC):
    @abstractmethod
    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]: ...
