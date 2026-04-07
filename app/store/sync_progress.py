from __future__ import annotations

from typing import Optional
from app.models import SyncProgress


class SyncProgressStore:
    def __init__(self) -> None:
        self._store: dict[str, SyncProgress] = {}

    def set(self, source_id: str, progress: SyncProgress) -> None:
        self._store[source_id] = progress

    def get(self, source_id: str) -> Optional[SyncProgress]:
        return self._store.get(source_id)

    def clear(self, source_id: str) -> None:
        self._store.pop(source_id, None)
