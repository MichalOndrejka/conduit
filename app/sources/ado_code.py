from __future__ import annotations

import fnmatch
import re
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
        # Normalise separators
        path_n = path.replace("\\", "/")
        pat_n = pattern.replace("\\", "/")
        if fnmatch.fnmatch(path_n, pat_n):
            return True
        # Allow ** to match across path separators
        regex = "^" + re.escape(pat_n).replace(r"\*\*", ".*").replace(r"\*", "[^/]*") + "$"
        if re.match(regex, path_n):
            return True
    return False


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
        branch = self._source.get_config(ConfigKeys.BRANCH) or "main"
        patterns_raw = self._source.get_config(ConfigKeys.GLOB_PATTERNS) or _DEFAULT_PATTERNS
        patterns = [p.strip() for p in patterns_raw.split(",") if p.strip()]

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message="Fetching file tree…"))

        tree = await self._client.get_file_tree(conn, repository, branch)
        matched = [f for f in tree if _glob_matches(f.get("path", ""), patterns)]

        if progress_cb:
            progress_cb(SyncProgress(
                phase="fetching",
                message=f"Found {len(matched)} files matching patterns",
            ))

        docs: list[SourceDocument] = []
        for i, file_info in enumerate(matched):
            path = file_info.get("path", "")
            if progress_cb:
                progress_cb(SyncProgress(
                    phase="fetching",
                    message=f"Fetching {path} ({i + 1}/{len(matched)})",
                ))

            try:
                content = await self._client.get_file_content(conn, repository, branch, path)
            except Exception:
                continue

            units = self._registry.parse(content, path)
            for unit in units:
                docs.append(SourceDocument(
                    id=f"{self._source.id}_{path}_{unit.to_id_slug()}",
                    text=unit.enriched_text,
                    tags={
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
