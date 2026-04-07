from __future__ import annotations

import re
from typing import Optional

from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


_XML_TAG_RE = re.compile(r"<[^>]+>")


def _strip_xml(text: str) -> str:
    return _XML_TAG_RE.sub(" ", text).strip()


class AdoTestCaseSource(Source):
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
            progress_cb(SyncProgress(phase="fetching", message="Running test case query…"))

        items = await self._client.run_work_item_query(conn, wiql, fields)

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetched {len(items)} test cases"))

        docs: list[SourceDocument] = []
        for item in items:
            fields_data: dict = item.get("fields", {})
            item_id = str(item.get("id", ""))
            title = fields_data.get("System.Title", "")
            state = fields_data.get("System.State", "")
            automation_status = fields_data.get("Microsoft.VSTS.TCM.AutomationStatus", "")
            steps_xml = fields_data.get("Microsoft.VSTS.TCM.Steps", "") or ""

            text_parts = [f"Test Case {item_id}: {title}"]
            if steps_xml:
                text_parts.append("Steps: " + _strip_xml(steps_xml))
            for k, v in fields_data.items():
                if v and str(v).strip() and k not in ("Microsoft.VSTS.TCM.Steps",):
                    text_parts.append(f"{k}: {v}")

            docs.append(SourceDocument(
                id=f"{self._source.id}_tc_{item_id}",
                text="\n".join(text_parts),
                tags={
                    "source_name": self._source.name,
                    "automation_status": automation_status,
                    "state": state,
                },
                properties={
                    "id": item_id,
                    "title": title,
                    "url": f"{conn.base_url}/_workitems/edit/{item_id}",
                },
            ))

        return docs
