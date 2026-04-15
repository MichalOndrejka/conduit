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


def _zip_root_prefix(zf: zipfile.ZipFile, patterns: list[str] | None = None) -> str:
    """Return the common root folder prefix to strip from zip entry paths, or '' if none.

    ADO sometimes wraps zip contents in a top-level folder named after the repo.
    We detect that folder and strip it so paths stay consistent with the file-tree API.
    However, we must NOT strip a folder that the user's glob patterns explicitly reference
    (e.g. pattern "SDN/**/*.md" means the user wants to match files under SDN/, so we
    should not treat SDN/ as a synthetic wrapper to discard).
    """
    names = [e.filename for e in zf.infolist() if not e.is_dir()]
    if not names:
        return ""
    roots = {n.split("/")[0] for n in names}
    # A common root exists when every entry shares one first path component AND
    # at least one entry actually lives inside a folder (has a "/" in the name).
    if len(roots) == 1 and any("/" in n for n in names):
        candidate = roots.pop()
        # Don't strip if any glob pattern explicitly names this directory as its first component.
        if patterns:
            for p in patterns:
                first = p.strip().lstrip("/").lstrip("\\").replace("\\", "/").split("/")[0]
                if first == candidate:
                    return ""
        return candidate + "/"
    return ""


def _extract_matched(zip_bytes: bytes, patterns: list[str]) -> dict[str, str]:
    """Extract files matching patterns from a zip archive. Returns {path: content}."""
    result: dict[str, str] = {}
    with zipfile.ZipFile(io.BytesIO(zip_bytes)) as zf:
        prefix = _zip_root_prefix(zf, patterns)
        for entry in zf.infolist():
            if entry.is_dir():
                continue
            name = entry.filename
            # Strip the common root folder if present (ADO often prepends one)
            relative = name[len(prefix):] if prefix and name.startswith(prefix) else name
            path = "/" + relative
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
