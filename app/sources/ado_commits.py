from __future__ import annotations

from typing import Optional
from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


class AdoGitCommitsSource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        repo = self._source.get_config(ConfigKeys.REPOSITORY)
        branch = self._source.get_config(ConfigKeys.BRANCH, "main")
        top = int(self._source.get_config(ConfigKeys.LAST_N_COMMITS, "100"))

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetching last {top} commits…"))

        commits = await self._client.get_commits(conn, repo, branch, top)

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetched {len(commits)} commits"))

        docs: list[SourceDocument] = []
        for commit in commits:
            commit_id = commit.get("commitId", "")
            short_id = commit_id[:8]
            message = (commit.get("comment") or "").strip()
            author_name = (commit.get("author") or {}).get("name", "")
            author_email = (commit.get("author") or {}).get("email", "")
            commit_date = ((commit.get("author") or {}).get("date") or "")[:10]
            change_counts = commit.get("changeCounts") or {}
            adds = change_counts.get("Add", 0)
            edits = change_counts.get("Edit", 0)
            deletes = change_counts.get("Delete", 0)

            text_parts = [f"Commit {short_id}: {message}"]
            text_parts.append(f"Author: {author_name} <{author_email}>")
            if commit_date:
                text_parts.append(f"Date: {commit_date}")
            if any([adds, edits, deletes]):
                text_parts.append(f"Changes: +{adds} files, ~{edits} files, -{deletes} files")

            docs.append(SourceDocument(
                id=f"{self._source.id}_commit_{short_id}",
                text="\n".join(text_parts),
                tags={
                    "source_id": self._source.id,
                    "source_name": self._source.name,
                    "author": author_name,
                    "repository": repo,
                },
                properties={
                    "id": short_id,
                    "title": message[:120],
                    "url": f"{conn.base_url}/_git/{repo}/commit/{commit_id}",
                },
            ))

        return docs
