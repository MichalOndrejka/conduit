from __future__ import annotations

from typing import Optional
from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


class AdoPipelineBuildSource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        pipeline_id = int(self._source.get_config(ConfigKeys.PIPELINE_ID) or "0")
        last_n = int(self._source.get_config(ConfigKeys.LAST_N_BUILDS) or "5")

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetching last {last_n} builds…"))

        builds = await self._client.get_builds(conn, pipeline_id, last_n)

        docs: list[SourceDocument] = []
        for build in builds:
            build_id = build.get("id")
            build_number = build.get("buildNumber", "")
            result = build.get("result", "")
            status = build.get("status", "")
            finish_time = build.get("finishTime", "")
            url = build.get("_links", {}).get("web", {}).get("href", "")

            text_parts = [
                f"Build #{build_number} — {result or status}",
                f"Pipeline ID: {pipeline_id}",
                f"Finish time: {finish_time}",
            ]

            # Fetch timeline for failed tasks
            if result in ("failed", "partiallySucceeded") and build_id:
                try:
                    timeline = await self._client.get_build_timeline(conn, build_id)
                    failed = [
                        r for r in timeline
                        if r.get("result") in ("failed", "abandoned")
                        and r.get("type") == "Task"
                    ]
                    if failed:
                        text_parts.append("\nFailed tasks:")
                        for task in failed:
                            text_parts.append(f"  - {task.get('name')}: {task.get('issues', '')}")
                except Exception:
                    pass

            docs.append(SourceDocument(
                id=f"{self._source.id}_build_{build_id}",
                text="\n".join(text_parts),
                tags={
                    "source_name": self._source.name,
                    "pipeline_id": str(pipeline_id),
                    "build_result": result,
                    "status": status,
                },
                properties={
                    "build_id": str(build_id),
                    "build_number": build_number,
                    "finish_time": finish_time,
                    "url": url,
                },
            ))

        return docs
