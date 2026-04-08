from __future__ import annotations

from typing import Optional
from app.ado.client import AdoClient, AdoConnection
from app.models import SourceDefinition, SourceDocument, SyncProgress, ConfigKeys
from app.sources.base import Source, ProgressCallback


class AdoTestResultsSource(Source):
    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        self._source = source
        self._client = client

    async def fetch_documents(
        self, progress_cb: Optional[ProgressCallback] = None
    ) -> list[SourceDocument]:
        conn = AdoConnection.from_config(self._source.config)
        last_n_runs = int(self._source.get_config(ConfigKeys.LAST_N_RUNS, "10"))
        results_per_run = int(self._source.get_config(ConfigKeys.RESULTS_PER_RUN, "200"))

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetching last {last_n_runs} test runs…"))

        runs = await self._client.get_test_runs(conn, last_n_runs)

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Found {len(runs)} runs — fetching results…"))

        docs: list[SourceDocument] = []
        for run in runs:
            run_id = run.get("id")
            run_name = run.get("name", f"Run {run_id}")
            run_state = run.get("state", "")
            completed = (run.get("completedDate") or "")[:10]

            results = await self._client.get_test_results(conn, run_id, results_per_run)

            for result in results:
                test_name = (result.get("testCase") or {}).get("name", result.get("testCaseTitle", ""))
                outcome = result.get("outcome", "")
                error_msg = (result.get("errorMessage") or "").strip()
                stack_trace = (result.get("stackTrace") or "").strip()
                duration_ms = result.get("durationInMs", 0)
                priority = result.get("priority", "")
                result_id = str(result.get("id", ""))

                text_parts = [f"Test: {test_name}"]
                text_parts.append(f"Run: {run_name}")
                text_parts.append(f"Outcome: {outcome}")
                if completed:
                    text_parts.append(f"Date: {completed}")
                if priority:
                    text_parts.append(f"Priority: {priority}")
                if duration_ms:
                    text_parts.append(f"Duration: {duration_ms:.0f}ms")
                if error_msg:
                    text_parts.append(f"Error: {error_msg}")
                if stack_trace:
                    text_parts.append(f"Stack trace:\n{stack_trace}")

                docs.append(SourceDocument(
                    id=f"{self._source.id}_tr_{run_id}_{result_id}",
                    text="\n".join(text_parts),
                    tags={
                        "source_id": self._source.id,
                        "source_name": self._source.name,
                        "outcome": outcome,
                        "run_name": run_name,
                    },
                    properties={
                        "id": result_id,
                        "title": test_name,
                        "url": f"{conn.base_url}/_testManagement/runs?runId={run_id}",
                    },
                ))

        if progress_cb:
            progress_cb(SyncProgress(phase="fetching", message=f"Fetched {len(docs)} test results"))

        return docs
