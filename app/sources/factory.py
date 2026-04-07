from __future__ import annotations

from app.ado.client import AdoClient
from app.models import SourceDefinition, SourceTypes, CollectionNames
from app.parsing.registry import ParserRegistry
from app.sources.base import Source
from app.sources.manual import ManualDocumentSource
from app.sources.ado_workitem import AdoWorkItemQuerySource
from app.sources.ado_code import AdoCodeRepoSource
from app.sources.ado_build import AdoPipelineBuildSource
from app.sources.ado_requirements import AdoRequirementsSource
from app.sources.ado_testcase import AdoTestCaseSource
from app.sources.ado_wiki import AdoWikiSource
from app.sources.http_page import HttpPageSource


def collection_for(source_type: str) -> str:
    return {
        SourceTypes.MANUAL_DOCUMENT:    CollectionNames.MANUAL_DOCUMENTS,
        SourceTypes.ADO_WORK_ITEM_QUERY: CollectionNames.ADO_WORK_ITEMS,
        SourceTypes.ADO_CODE_REPO:      CollectionNames.ADO_CODE,
        SourceTypes.ADO_PIPELINE_BUILD: CollectionNames.ADO_BUILDS,
        SourceTypes.ADO_REQUIREMENTS:   CollectionNames.ADO_REQUIREMENTS,
        SourceTypes.ADO_TEST_CASE:      CollectionNames.ADO_TEST_CASES,
        SourceTypes.ADO_WIKI:           CollectionNames.ADO_WIKI,
        SourceTypes.HTTP_PAGE:          CollectionNames.HTTP_PAGES,
    }.get(source_type, CollectionNames.MANUAL_DOCUMENTS)


SOURCE_TYPE_META = [
    (SourceTypes.MANUAL_DOCUMENT,    "Manual Document",        "Paste or upload text, Markdown, or PDF content."),
    (SourceTypes.ADO_WORK_ITEM_QUERY, "ADO Work Item Query",   "Index work items returned by a WIQL query."),
    (SourceTypes.ADO_CODE_REPO,      "ADO Code Repository",   "Index source files from an Azure DevOps Git repo."),
    (SourceTypes.ADO_PIPELINE_BUILD, "ADO Pipeline Builds",   "Index recent build results and failure details."),
    (SourceTypes.ADO_REQUIREMENTS,   "ADO Requirements",      "Index requirements work items from a WIQL query."),
    (SourceTypes.ADO_TEST_CASE,      "ADO Test Cases",        "Index test cases including steps from a WIQL query."),
    (SourceTypes.ADO_WIKI,           "ADO Wiki",              "Index pages from an Azure DevOps wiki."),
    (SourceTypes.HTTP_PAGE,          "HTTP Page",             "Index a single web page or JSON endpoint."),
]


class SourceFactory:
    def __init__(self, ado_client: AdoClient, parser_registry: ParserRegistry) -> None:
        self._ado = ado_client
        self._registry = parser_registry

    def create(self, source: SourceDefinition) -> Source:
        t = source.type
        if t == SourceTypes.MANUAL_DOCUMENT:
            return ManualDocumentSource(source)
        if t == SourceTypes.ADO_WORK_ITEM_QUERY:
            return AdoWorkItemQuerySource(source, self._ado)
        if t == SourceTypes.ADO_CODE_REPO:
            return AdoCodeRepoSource(source, self._ado, self._registry)
        if t == SourceTypes.ADO_PIPELINE_BUILD:
            return AdoPipelineBuildSource(source, self._ado)
        if t == SourceTypes.ADO_REQUIREMENTS:
            return AdoRequirementsSource(source, self._ado)
        if t == SourceTypes.ADO_TEST_CASE:
            return AdoTestCaseSource(source, self._ado)
        if t == SourceTypes.ADO_WIKI:
            return AdoWikiSource(source, self._ado)
        if t == SourceTypes.HTTP_PAGE:
            return HttpPageSource(source)
        raise ValueError(f"Unknown source type: {t}")
