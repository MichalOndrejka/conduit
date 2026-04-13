from __future__ import annotations

import re
from typing import Optional
from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


def _strip_html(value: str) -> str:
    """Remove HTML/XML tags and normalize whitespace."""
    text = re.sub(r"<[^>]+>", " ", value)
    text = re.sub(r"&[a-zA-Z]+;", " ", text)  # HTML entities like &nbsp;
    return re.sub(r"\s+", " ", text).strip()


_DEFAULT_ITEM_TYPES = ["Bug", "Task", "User Story", "Feature", "Epic"]


def _build_wiql(item_types: list[str], area_path: str, iteration_path: str = "") -> str:
    """Generate a WIQL query from item type filter and optional area/iteration paths."""
    conditions = ["[System.TeamProject] = @project"]
    if item_types:
        types_str = ", ".join(f"'{t}'" for t in item_types)
        conditions.append(f"[System.WorkItemType] IN ({types_str})")
    if area_path:
        conditions.append(f"[System.AreaPath] UNDER '{area_path}'")
    if iteration_path:
        conditions.append(f"[System.IterationPath] UNDER '{iteration_path}'")
    return f"SELECT [System.Id] FROM WorkItems WHERE {' AND '.join(conditions)} ORDER BY [System.ChangedDate] DESC"


class AdoWorkItemQuerySource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)

        # Custom WIQL overrides everything; otherwise build from item type filter
        custom_query = self._source.get_config(ConfigKeys.QUERY)
        if custom_query:
            wiql = custom_query
        else:
            raw_types = self._source.get_config(ConfigKeys.ITEM_TYPES)
            item_types = [t.strip() for t in raw_types.split(",") if t.strip()] if raw_types else _DEFAULT_ITEM_TYPES
            area_path = self._source.get_config(ConfigKeys.AREA_PATH)
            iteration_path = self._source.get_config(ConfigKeys.ITERATION_PATH)
            wiql = _build_wiql(item_types, area_path, iteration_path)

        fields_raw = self._source.get_config(ConfigKeys.FIELDS)
        fields = [f.strip() for f in fields_raw.split(",") if f.strip()]

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message="Running work item query…"))

        items = await self._client.run_work_item_query(conn, wiql, fields)

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetched {len(items)} work items"))

        docs: list[SourceDocument] = []
        for item in items:
            fields_data: dict = item.get("fields", {})
            item_id = str(item.get("id", ""))
            title = fields_data.get("System.Title", "")
            state = fields_data.get("System.State", "")
            wi_type = fields_data.get("System.WorkItemType", "")
            base_url = conn.base_url

            text_parts = [f"Work Item {item_id}: {title}"]
            for k, v in fields_data.items():
                if v and str(v).strip():
                    clean = _strip_html(str(v))
                    if clean:
                        text_parts.append(f"{k}: {clean}")

            docs.append(SourceDocument(
                id=f"{self._source.id}_wi_{item_id}",
                text="\n".join(text_parts),
                tags={
                    "source_id": self._source.id,
                    "source_name": self._source.name,
                    "work_item_type": wi_type,
                    "state": state,
                },
                properties={
                    "id": item_id,
                    "title": title,
                    "url": f"{base_url}/_workitems/edit/{item_id}",
                },
            ))

        return docs
