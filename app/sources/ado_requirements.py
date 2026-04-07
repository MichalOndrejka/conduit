from __future__ import annotations

from app.models import SourceDefinition
from app.ado.client import AdoClient
from app.sources.ado_workitem import AdoWorkItemQuerySource


class AdoRequirementsSource(AdoWorkItemQuerySource):
    """Same fetch logic as work-item query; the collection differs."""

    def __init__(self, source: SourceDefinition, client: AdoClient) -> None:
        super().__init__(source, client)
