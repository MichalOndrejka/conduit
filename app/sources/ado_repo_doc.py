from __future__ import annotations

import io
import os as _os
import zipfile
from typing import Optional

from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.parsing.registry import ParserRegistry
from app.sources.base import Source, ProgressCallback
from app.sources.ado_code import _glob_matches

_DEFAULT_PATTERNS = "**/*.md"


def _extract_matched(zip_bytes: bytes, patterns: list[str]) -> dict[str, str]:
    result: dict[str, str] = {}
    with zipfile.ZipFile(io.BytesIO(zip_bytes)) as zf:
        for entry in zf.infolist():
            if entry.is_dir():
                continue
            parts = entry.filename.split("/", 1)
            path = "/" + (parts[1] if len(parts) > 1 else parts[0])
            if not _glob_matches(path, patterns):
                continue
            try:
                content = zf.read(entry).decode("utf-8", errors="replace")
            except Exception:
                continue
            result[path] = content
    return result


class AdoRepoDocSource(Source):
    def __init__(
        self,
        source: SourceDefinition,
        client: AdoClient,
        parser_registry: ParserRegistry,
    ) -> None:
        self._source = source
        self._client = client
        self._registry = parser_registry

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        repository = self._source.get_config(ConfigKeys.REPOSITORY)
        branch = self._source.get_config(ConfigKeys.BRANCH)
        patterns_raw = self._source.get_config(ConfigKeys.GLOB_PATTERNS) or _DEFAULT_PATTERNS
        patterns = [p.strip() for p in patterns_raw.split(",") if p.strip()]

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message="Downloading repository zip…"))

        zip_bytes = await self._client.get_repo_zip(conn, repository, branch)

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message="Extracting matched files…"))

        files = _extract_matched(zip_bytes, patterns)

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Parsing {len(files)} matched files…"))

        docs: list[SourceDocument] = []
        for path, content in files.items():
            units = self._registry.parse(content, path)
            for unit in units:
                docs.append(SourceDocument(
                    id=f"{self._source.id}_{path}_{unit.to_id_slug()}",
                    text=unit.full_text,
                    tags={
                        "source_id": self._source.id,
                        "source_name": self._source.name,
                    },
                    properties={
                        "title": unit.name,
                        "file_path": path,
                        "repository": repository,
                    },
                ))
        return docs

    async def preview_documents(self) -> list[SourceDocument]:
        import asyncio

        _PREVIEW_LIMIT = 5

        conn = AdoConnection.from_config(self._source.config)
        repository = self._source.get_config(ConfigKeys.REPOSITORY)
        branch = self._source.get_config(ConfigKeys.BRANCH)
        patterns_raw = self._source.get_config(ConfigKeys.GLOB_PATTERNS) or _DEFAULT_PATTERNS
        patterns = [p.strip() for p in patterns_raw.split(",") if p.strip()]

        tree = await self._client.get_file_tree(conn, repository, branch)
        matched = [f for f in tree if _glob_matches(f.get("path", ""), patterns)]
        sample = matched[:_PREVIEW_LIMIT]

        matched_total = len(matched)

        async def fetch_one(file_info: dict, is_first: bool) -> Optional[SourceDocument]:
            path = file_info.get("path", "")
            try:
                content = await self._client.get_file_content(conn, repository, branch, path)
            except Exception:
                return None
            props = {
                "title": _os.path.basename(path),
                "file_path": path,
                "repository": repository,
            }
            if is_first:
                props["__matched_total__"] = str(matched_total)
            return SourceDocument(
                id=f"{self._source.id}_{path}",
                text=content[:2000],
                tags={
                    "source_id": self._source.id,
                    "source_name": self._source.name,
                },
                properties=props,
            )

        results = await asyncio.gather(*[fetch_one(f, i == 0) for i, f in enumerate(sample)])
        return [doc for doc in results if doc is not None]
