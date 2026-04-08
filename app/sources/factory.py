from __future__ import annotations

from typing import NamedTuple

from app.ado.client import AdoClient
from app.models import SourceDefinition, SourceTypes, CollectionNames
from app.parsing.registry import ParserRegistry
from app.sources.base import Source
from app.sources.ado_workitem import AdoWorkItemQuerySource
from app.sources.ado_testcase import AdoTestCaseSource
from app.sources.ado_code import AdoCodeRepoSource
from app.sources.ado_build import AdoPipelineBuildSource
from app.sources.ado_wiki import AdoWikiSource
from app.sources.ado_pullrequest import AdoPullRequestSource
from app.sources.ado_testresults import AdoTestResultsSource
from app.sources.ado_commits import AdoGitCommitsSource


# ── Registry types ─────────────────────────────────────────────────────────────

class SourceTypeMeta(NamedTuple):
    type: str           # internal identifier stored in source config
    label: str          # short display name
    description: str    # one-line description shown in the picker
    provider: str       # platform key (see PLATFORMS)


# ── Platforms ──────────────────────────────────────────────────────────────────

PLATFORMS: dict[str, dict] = {
    "ado": {
        "label": "Azure DevOps",
        "description": "Work items, code repos, wikis, builds and test cases — cloud and on-premise TFS/VSTS",
        "available": True,
    },
}

PROVIDERS: dict[str, dict] = {
    "ado": {"label": "Azure DevOps"},
}


# ── Source type registry ───────────────────────────────────────────────────────

SOURCE_TYPE_META: list[SourceTypeMeta] = [
    SourceTypeMeta(SourceTypes.WORK_ITEM_QUERY, "Work Items",     "Bugs, tasks, user stories, features and epics. Filter by type or supply a custom WIQL query.", "ado"),
    SourceTypeMeta(SourceTypes.TEST_CASE,       "Test Cases",     "Test case definitions with steps and automation status.", "ado"),
    SourceTypeMeta(SourceTypes.TEST_RESULTS,    "Test Results",   "Runtime test execution results — pass/fail outcomes, error messages and stack traces.", "ado"),
    SourceTypeMeta(SourceTypes.PULL_REQUEST,    "Pull Requests",  "PR titles, descriptions, reviewers and branch context.", "ado"),
    SourceTypeMeta(SourceTypes.GIT_COMMITS,     "Git Commits",    "Commit history with messages, authors and change counts.", "ado"),
    SourceTypeMeta(SourceTypes.CODE_REPO,       "Source Code",    "Source files from a git repository, filtered by glob patterns.", "ado"),
    SourceTypeMeta(SourceTypes.WIKI,            "Wiki & Docs",    "Pages from an Azure DevOps wiki, with optional path filter.", "ado"),
    SourceTypeMeta(SourceTypes.PIPELINE_BUILD,  "Build Results",  "Recent CI/CD build logs and failure details for a pipeline.", "ado"),
]


def collection_for(source: SourceDefinition) -> str:
    return {
        SourceTypes.WORK_ITEM_QUERY: CollectionNames.WORK_ITEMS,
        SourceTypes.TEST_CASE:       CollectionNames.TEST_CASES,
        SourceTypes.TEST_RESULTS:    CollectionNames.TEST_RESULTS,
        SourceTypes.PULL_REQUEST:    CollectionNames.PULL_REQUESTS,
        SourceTypes.GIT_COMMITS:     CollectionNames.COMMITS,
        SourceTypes.CODE_REPO:       CollectionNames.CODE,
        SourceTypes.PIPELINE_BUILD:  CollectionNames.BUILDS,
        SourceTypes.WIKI:            CollectionNames.WIKI,
    }.get(source.type, CollectionNames.WORK_ITEMS)


class SourceFactory:
    def __init__(self, ado_client: AdoClient, parser_registry: ParserRegistry) -> None:
        self._ado = ado_client
        self._registry = parser_registry

    def create(self, source: SourceDefinition) -> Source:
        t = source.type
        if t == SourceTypes.WORK_ITEM_QUERY:
            return AdoWorkItemQuerySource(source, self._ado)
        if t == SourceTypes.TEST_CASE:
            return AdoTestCaseSource(source, self._ado)
        if t == SourceTypes.CODE_REPO:
            return AdoCodeRepoSource(source, self._ado, self._registry)
        if t == SourceTypes.PIPELINE_BUILD:
            return AdoPipelineBuildSource(source, self._ado)
        if t == SourceTypes.WIKI:
            return AdoWikiSource(source, self._ado)
        if t == SourceTypes.PULL_REQUEST:
            return AdoPullRequestSource(source, self._ado)
        if t == SourceTypes.TEST_RESULTS:
            return AdoTestResultsSource(source, self._ado)
        if t == SourceTypes.GIT_COMMITS:
            return AdoGitCommitsSource(source, self._ado)
        raise ValueError(f"Unknown source type: {t}")
