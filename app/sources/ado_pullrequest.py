from __future__ import annotations

from typing import Optional
from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


class AdoPullRequestSource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        repo = self._source.get_config(ConfigKeys.REPOSITORY)
        status = self._source.get_config(ConfigKeys.STATUS_FILTER, "all")
        top = int(self._source.get_config(ConfigKeys.TOP, "200"))

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message="Fetching pull requests…"))

        prs = await self._client.get_pull_requests(conn, repo, status, top)

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetched {len(prs)} pull requests"))

        docs: list[SourceDocument] = []
        for pr in prs:
            pr_id = str(pr.get("pullRequestId", ""))
            title = pr.get("title", "")
            description = (pr.get("description") or "").strip()
            status_val = pr.get("status", "")
            source_branch = pr.get("sourceRefName", "").replace("refs/heads/", "")
            target_branch = pr.get("targetRefName", "").replace("refs/heads/", "")
            author = pr.get("createdBy", {}).get("displayName", "")
            reviewers = [r.get("displayName", "") for r in pr.get("reviewers", [])]
            created = (pr.get("creationDate") or "")[:10]
            closed = (pr.get("closedDate") or "")[:10]

            text_parts = [f"Pull Request #{pr_id}: {title}"]
            if description:
                text_parts.append(f"Description: {description}")
            text_parts.append(f"Author: {author}")
            text_parts.append(f"Status: {status_val}")
            text_parts.append(f"Branch: {source_branch} → {target_branch}")
            if reviewers:
                text_parts.append(f"Reviewers: {', '.join(reviewers)}")
            if created:
                text_parts.append(f"Created: {created}")
            if closed:
                text_parts.append(f"Closed: {closed}")

            docs.append(SourceDocument(
                id=f"{self._source.id}_pr_{pr_id}",
                text="\n".join(text_parts),
                tags={
                    "source_id": self._source.id,
                    "source_name": self._source.name,
                    "status": status_val,
                    "author": author,
                },
                properties={
                    "id": pr_id,
                    "title": title,
                    "url": f"{conn.base_url}/_git/{repo}/pullrequest/{pr_id}",
                },
            ))

        return docs
