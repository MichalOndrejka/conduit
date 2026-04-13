from __future__ import annotations

from typing import Optional
from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


class AdoReleaseSource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        definition_id = int(self._source.get_config(ConfigKeys.RELEASE_DEFINITION_ID) or "0")
        last_n = int(self._source.get_config(ConfigKeys.LAST_N_RELEASES) or "5")

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetching last {last_n} releases…"))

        releases = await self._client.get_releases(conn, definition_id, last_n)

        docs: list[SourceDocument] = []
        for release in releases:
            release_id = release.get("id")
            release_name = release.get("name", "")
            status = release.get("status", "")
            created_on = release.get("createdOn", "")
            description = release.get("description", "")
            url = release.get("_links", {}).get("web", {}).get("href", "")

            text_parts = [
                f"Release: {release_name} — {status}",
                f"Definition ID: {definition_id}",
                f"Created: {created_on}",
            ]
            if description:
                text_parts.append(f"Description: {description}")

            # Include environment deployment outcomes
            environments = release.get("environments", [])
            if environments:
                text_parts.append("\nEnvironments:")
                for env in environments:
                    env_name = env.get("name", "")
                    env_status = env.get("status", "")
                    text_parts.append(f"  - {env_name}: {env_status}")

            docs.append(SourceDocument(
                id=f"{self._source.id}_release_{release_id}",
                text="\n".join(text_parts),
                tags={
                    "source_id": self._source.id,
                    "source_name": self._source.name,
                    "definition_id": str(definition_id),
                    "release_status": status,
                },
                properties={
                    "release_id": str(release_id),
                    "release_name": release_name,
                    "created_on": created_on,
                    "url": url,
                },
            ))

        return docs
