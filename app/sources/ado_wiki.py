from __future__ import annotations

from typing import Optional

from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.parsing.markdown import MarkdownParser
from app.sources.base import Source, ProgressCallback


class AdoWikiSource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client
        self._md_parser = MarkdownParser()

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        wiki_name = self._source.get_config(ConfigKeys.WIKI_NAME)
        path_filter = self._source.get_config(ConfigKeys.PATH_FILTER) or "/"

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message="Fetching wiki list…"))

        wikis = await self._client.get_wikis(conn)
        if not wikis:
            return []

        wiki = next(
            (w for w in wikis if w.get("name") == wiki_name),
            wikis[0],
        ) if wiki_name else wikis[0]

        wiki_id = wiki.get("id", "")
        wiki_actual_name = wiki.get("name", "")

        if progress_cb:
            progress_cb(SyncProgress(
                phase="fetching",
                message=f"Fetching pages from wiki '{wiki_actual_name}'…",
            ))

        pages = await self._client.get_wiki_items(conn, wiki_id, path_filter)

        docs: list[SourceDocument] = []
        self._collect_pages(pages, conn, wiki_id, wiki_actual_name, docs, progress_cb)
        return docs

    def _collect_pages(
        self,
        pages: list[dict],
        conn: AdoConnection,
        wiki_id: str,
        wiki_name: str,
        docs: list[SourceDocument],
        progress_cb,
    ) -> None:
        for page in pages:
            path = page.get("path", "/")
            content = page.get("content", "") or ""
            title = path.rstrip("/").split("/")[-1] or wiki_name

            if content.strip():
                # Parse into sections
                units = self._md_parser.parse(content, path)
                for unit in units:
                    section = unit.name if unit.name != title else ""
                    docs.append(SourceDocument(
                        id=f"{self._source.id}_{path}_{unit.to_id_slug()}",
                        text=unit.full_text,
                        tags={
                            "source_id": self._source.id,
                            "source_name": self._source.name,
                            "wiki_name": wiki_name,
                            "section": section,
                        },
                        properties={
                            "title": title,
                            "path": path,
                            "wiki_name": wiki_name,
                        },
                    ))

            # Recurse into sub-pages
            sub_pages = page.get("subPages", []) or []
            if sub_pages:
                self._collect_pages(sub_pages, conn, wiki_id, wiki_name, docs, progress_cb)
