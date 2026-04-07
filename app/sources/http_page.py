from __future__ import annotations

import asyncio
import hashlib
import json
import os
from typing import Optional
from urllib.parse import urlparse

import requests
from requests.auth import HTTPBasicAuth

from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


def _fnv1a(text: str) -> str:
    h = 2166136261
    for b in text.encode():
        h ^= b
        h = (h * 16777619) & 0xFFFFFFFF
    return format(h, "08x")


class HttpPageSource(Source):
    def __init__(self, source: SourceDefinition) -> None:
        self._source = source

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        url = self._source.get_config(ConfigKeys.URL)
        title = self._source.get_config(ConfigKeys.TITLE) or urlparse(url).netloc
        content_type = self._source.get_config(ConfigKeys.CONTENT_TYPE) or "auto"

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetching {url}…"))

        text = await asyncio.to_thread(self._sync_fetch, url, content_type)
        doc_id = _fnv1a(url)

        return [SourceDocument(
            id=f"{self._source.id}_{doc_id}",
            text=text,
            tags={"source_name": self._source.name},
            properties={"title": title, "url": url},
        )]

    def _sync_fetch(self, url: str, content_type: str) -> str:
        session = requests.Session()
        auth_type = self._source.get_config(ConfigKeys.AUTH_TYPE, "none").lower()

        if auth_type == "pat":
            pat = os.environ.get(self._source.get_config(ConfigKeys.PAT), "")
            session.auth = HTTPBasicAuth("", pat)
        elif auth_type == "bearer":
            token = os.environ.get(self._source.get_config(ConfigKeys.TOKEN), "")
            session.headers["Authorization"] = f"Bearer {token}"
        elif auth_type == "apikey":
            header = self._source.get_config(ConfigKeys.API_KEY_HEADER)
            value = os.environ.get(self._source.get_config(ConfigKeys.API_KEY_VALUE), "")
            session.headers[header] = value

        resp = session.get(url, timeout=30)
        resp.raise_for_status()

        detected = content_type
        if detected == "auto":
            ct = resp.headers.get("Content-Type", "")
            if "html" in ct:
                detected = "html"
            elif "json" in ct:
                detected = "json"
            else:
                detected = "text"

        if detected == "html":
            from bs4 import BeautifulSoup
            soup = BeautifulSoup(resp.text, "lxml")
            for tag in soup(["script", "style", "nav", "footer", "header"]):
                tag.decompose()
            return soup.get_text(separator="\n", strip=True)
        elif detected == "json":
            try:
                return json.dumps(resp.json(), indent=2)
            except Exception:
                return resp.text
        else:
            return resp.text
