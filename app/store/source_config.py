from __future__ import annotations

import asyncio
import json
from pathlib import Path
from typing import Optional

from app.models import ConfigKeys, DOCUMENT_PLACEHOLDER, SourceDefinition


class SourceConfigStore:
    def __init__(self, file_path: str) -> None:
        self._path = Path(file_path)
        self._lock = asyncio.Lock()

    async def get_all(self) -> list[SourceDefinition]:
        async with self._lock:
            return self._read()

    async def get_by_id(self, source_id: str) -> Optional[SourceDefinition]:
        async with self._lock:
            return next((s for s in self._read() if s.id == source_id), None)

    async def save(self, source: SourceDefinition) -> None:
        async with self._lock:
            sources = self._read()
            idx = next((i for i, s in enumerate(sources) if s.id == source.id), None)
            if idx is not None:
                sources[idx] = source
            else:
                sources.append(source)
            self._write(sources)

    async def delete(self, source_id: str) -> None:
        async with self._lock:
            self._write([s for s in self._read() if s.id != source_id])

    async def reset_all_sync_status(self, status: str) -> None:
        async with self._lock:
            sources = self._read()
            for s in sources:
                s.sync_status = status
                s.sync_error = None
            self._write(sources)

    async def reorder(self, ids: list[str]) -> None:
        async with self._lock:
            sources = self._read()
            id_map = {s.id: s for s in sources}
            ordered = [id_map[i] for i in ids if i in id_map]
            mentioned = set(ids)
            tail = [s for s in sources if s.id not in mentioned]
            self._write(ordered + tail)

    def export_stripped(self) -> list[dict]:
        result = []
        for s in self._read():
            d = s.model_dump(mode="json")
            cfg = d.get("config", {})
            if cfg.get(ConfigKeys.PROVIDER) == "manual" or cfg.get(ConfigKeys.DOC_TYPE) == "upload":
                cfg[ConfigKeys.CONTENT] = DOCUMENT_PLACEHOLDER
            result.append(d)
        return result

    def _read(self) -> list[SourceDefinition]:
        if not self._path.exists():
            return []
        try:
            data = json.loads(self._path.read_text(encoding="utf-8"))
            # Support new {"sources": [...]} format (migration back to flat)
            if isinstance(data, dict):
                data = data.get("sources", [])
            result = []
            for item in data:
                item = _normalise_keys(item)
                result.append(SourceDefinition.model_validate(item))
            return result
        except Exception:
            return []

    def _write(self, sources: list[SourceDefinition]) -> None:
        self._path.write_text(
            json.dumps(
                [s.model_dump(mode="json") for s in sources],
                indent=2,
                default=str,
            ),
            encoding="utf-8",
        )


def _normalise_keys(d: dict) -> dict:
    """Accept camelCase keys from C# exports and convert to snake_case."""
    mapping = {
        "lastSyncedAt": "last_synced_at",
        "syncStatus": "sync_status",
        "syncError": "sync_error",
        "syncErrorPhase": "sync_error_phase",
    }
    return {mapping.get(k, k): v for k, v in d.items()}
