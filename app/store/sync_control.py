from __future__ import annotations

import asyncio


class SyncCancelled(Exception):
    pass


class SyncControlStore:
    def __init__(self) -> None:
        self._cancelled: set[str] = set()
        self._pause_events: dict[str, asyncio.Event] = {}

    def _get_event(self, source_id: str) -> asyncio.Event:
        if source_id not in self._pause_events:
            ev = asyncio.Event()
            ev.set()
            self._pause_events[source_id] = ev
        return self._pause_events[source_id]

    def register(self, source_id: str) -> None:
        """Reset control state for a new sync run."""
        self._cancelled.discard(source_id)
        ev = asyncio.Event()
        ev.set()
        self._pause_events[source_id] = ev

    def request_cancel(self, source_id: str) -> None:
        self._cancelled.add(source_id)
        if source_id in self._pause_events:
            self._pause_events[source_id].set()

    def pause(self, source_id: str) -> None:
        self._get_event(source_id).clear()

    def resume(self, source_id: str) -> None:
        self._get_event(source_id).set()

    def is_paused(self, source_id: str) -> bool:
        ev = self._pause_events.get(source_id)
        return ev is not None and not ev.is_set()

    async def checkpoint(self, source_id: str) -> None:
        """Wait if paused. Raises SyncCancelled if cancelled."""
        ev = self._get_event(source_id)
        if not ev.is_set():
            await ev.wait()
        if source_id in self._cancelled:
            raise SyncCancelled()

    def clear(self, source_id: str) -> None:
        self._cancelled.discard(source_id)
        self._pause_events.pop(source_id, None)
