from __future__ import annotations

from typing import Optional

import httpx

from app.models import SourceDefinition, ConfigKeys, SourceDocument
from app.sources.base import Source, ProgressCallback


class CustomApiSource(Source):
    """Generic HTTP API source — fetches a JSON endpoint and maps items to documents."""

    def __init__(self, source: SourceDefinition) -> None:
        self._source = source

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        return await self._fetch()

    async def preview_documents(self) -> list[SourceDocument]:
        return await self._fetch()

    async def _fetch(self) -> list[SourceDocument]:
        cfg = self._source
        url = cfg.get_config(ConfigKeys.URL)
        if not url:
            raise ValueError("Custom API source requires a URL")

        method      = cfg.get_config(ConfigKeys.HTTP_METHOD, "GET").upper()
        auth_type   = cfg.get_config(ConfigKeys.AUTH_TYPE, "none").lower()
        items_path  = cfg.get_config(ConfigKeys.ITEMS_PATH, "")
        title_field = cfg.get_config(ConfigKeys.TITLE_FIELD, "title")
        content_raw = cfg.get_config(ConfigKeys.CONTENT_FIELDS, "")
        content_fields = [f.strip() for f in content_raw.split(",") if f.strip()]

        headers = self._build_headers(auth_type)

        async with httpx.AsyncClient(follow_redirects=True) as client:
            if method == "POST":
                resp = await client.post(url, headers=headers, timeout=30)
            else:
                resp = await client.get(url, headers=headers, timeout=30)
            resp.raise_for_status()
            data = resp.json()

        items = self._navigate(data, items_path)
        if not isinstance(items, list):
            items = [items] if items else []

        docs: list[SourceDocument] = []
        for i, item in enumerate(items):
            title = self._field(item, title_field) or f"Item {i + 1}"

            if content_fields:
                parts = [f"{f}: {self._field(item, f)}" for f in content_fields if self._field(item, f)]
            elif isinstance(item, dict):
                parts = [f"{k}: {v}" for k, v in item.items() if k != title_field]
            else:
                parts = [str(item)]

            text = f"{title}\n" + "\n".join(str(p) for p in parts)

            docs.append(SourceDocument(
                id=f"{self._source.id}_capi_{i}",
                text=text.strip(),
                tags={
                    "source_id":   self._source.id,
                    "source_name": self._source.name,
                },
                properties={
                    "title": title,
                    "url":   url,
                },
            ))

        return docs

    def _get_secret(self, cred_id: str) -> str:
        if not cred_id:
            return ""
        from app import container
        return container.secrets_store.get_value_sync(cred_id)

    def _build_headers(self, auth_type: str) -> dict[str, str]:
        headers: dict[str, str] = {}
        cfg = self._source
        if auth_type == "bearer":
            token = self._get_secret(cfg.get_config(ConfigKeys.TOKEN))
            headers["Authorization"] = f"Bearer {token}"
        elif auth_type == "apikey":
            header_name = cfg.get_config(ConfigKeys.API_KEY_HEADER, "X-Api-Key")
            value = self._get_secret(cfg.get_config(ConfigKeys.API_KEY_VALUE))
            headers[header_name] = value
        return headers

    @staticmethod
    def _navigate(data: object, path: str) -> object:
        if not path:
            return data
        for key in path.split("."):
            if isinstance(data, dict):
                data = data.get(key)
            else:
                return []
            if data is None:
                return []
        return data

    @staticmethod
    def _field(item: object, name: str) -> str:
        if isinstance(item, dict):
            return str(item.get(name, "")) if item.get(name) is not None else ""
        return ""
