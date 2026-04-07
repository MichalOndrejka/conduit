from __future__ import annotations

from typing import Optional
from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


class AdoWorkItemQuerySource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        wiql = self._source.get_config(ConfigKeys.QUERY)
        fields_raw = self._source.get_config(ConfigKeys.FIELDS)
        fields = [f.strip() for f in fields_raw.split(",") if f.strip()]

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message="Running WIQL query…"))

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
                    text_parts.append(f"{k}: {v}")
            text = "\n".join(text_parts)

            docs.append(SourceDocument(
                id=f"{self._source.id}_wi_{item_id}",
                text=text,
                tags={
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
