from __future__ import annotations

import fnmatch
import io
import os as _os
import re
import zipfile
from typing import Optional

from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.parsing.registry import ParserRegistry
from app.sources.base import Source, ProgressCallback

_DEFAULT_PATTERNS = "**/*.cs"


def _glob_matches(path: str, patterns: list[str]) -> bool:
    path = path.lstrip("/").lstrip("\\")
    for pattern in patterns:
        pattern = pattern.strip().lstrip("/").lstrip("\\")
        path_n = path.replace("\\", "/")
        pat_n = pattern.replace("\\", "/")
        if fnmatch.fnmatch(path_n, pat_n):
            return True
        # Allow ** to match across path separators (including zero directories)
        regex = "^" + re.escape(pat_n).replace(r"\*\*/", "(.*/)?").replace(r"\*\*", ".*").replace(r"\*", "[^/]*") + "$"
        if re.match(regex, path_n):
            return True
    return False


def _extract_matched(zip_bytes: bytes, patterns: list[str]) -> dict[str, str]:
    """Extract files matching patterns from a zip archive. Returns {path: content}."""
    result: dict[str, str] = {}
    with zipfile.ZipFile(io.BytesIO(zip_bytes)) as zf:
        for entry in zf.infolist():
            if entry.is_dir():
                continue
            # ADO zips prepend a root folder; strip the first path component
            name = entry.filename
            parts = name.split("/", 1)
            path = "/" + (parts[1] if len(parts) > 1 else parts[0])
            if not _glob_matches(path, patterns):
                continue
            try:
                content = zf.read(entry).decode("utf-8", errors="replace")
            except Exception:
                continue
            result[path] = content
    return result


class AdoCodeRepoSource(Source):
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
            progress_cb(SyncProgress(
                phase="fetching",
                message=f"Parsing {len(files)} matched files…",
            ))

        docs: list[SourceDocument] = []
        for path, content in files.items():
            units = self._registry.parse(content, path)
            for unit in units:
                docs.append(SourceDocument(
                    id=f"{self._source.id}_{path}_{unit.to_id_slug()}",
                    text=unit.enriched_text,
                    tags={
                        "source_id": self._source.id,
                        "source_name": self._source.name,
                        "language": unit.language,
                        "kind": unit.kind.value,
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

        # Use the file tree (1 call) + fetch only the sample files concurrently —
        # much cheaper than downloading the full repo zip for just 5 files.
        tree = await self._client.get_file_tree(conn, repository, branch)
        matched = [f for f in tree if _glob_matches(f.get("path", ""), patterns)]
        sample = matched[:_PREVIEW_LIMIT]

        async def fetch_one(file_info: dict) -> Optional[SourceDocument]:
            path = file_info.get("path", "")
            try:
                content = await self._client.get_file_content(conn, repository, branch, path)
            except Exception:
                return None
            return SourceDocument(
                id=f"{self._source.id}_{path}",
                text=content[:2000],
                tags={
                    "source_id": self._source.id,
                    "source_name": self._source.name,
                },
                properties={
                    "title": _os.path.basename(path),
                    "file_path": path,
                    "repository": repository,
                    "preview_note": f"Showing {len(sample)} of {len(matched)} matched files",
                },
            )

        results = await asyncio.gather(*[fetch_one(f) for f in sample])
        return [doc for doc in results if doc is not None]
