import pytest
from unittest.mock import MagicMock

from app.models import CollectionNames, ConfigKeys, SourceDefinition, SourceTypes
from app.sources.ado_build import AdoPipelineBuildSource
from app.sources.ado_code import AdoCodeRepoSource
from app.sources.ado_commits import AdoGitCommitsSource
from app.sources.ado_pullrequest import AdoPullRequestSource
from app.sources.ado_testresults import AdoTestResultsSource
from app.sources.ado_wiki import AdoWikiSource
from app.sources.ado_workitem import AdoWorkItemQuerySource
from app.sources.custom_api import CustomApiSource
from app.sources.factory import SourceFactory, collection_for
from app.sources.manual import ManualDocumentSource


def _factory() -> SourceFactory:
    return SourceFactory(ado_client=MagicMock(), parser_registry=MagicMock())


def _source(type_: str, **config) -> SourceDefinition:
    return SourceDefinition(type=type_, name="test", config=config)


# ── Provider overrides ────────────────────────────────────────────────────────

def test_create_custom_provider_returns_custom_api_source():
    src = _source(SourceTypes.WORK_ITEM_QUERY, **{ConfigKeys.PROVIDER: "custom", ConfigKeys.URL: "http://x"})
    assert isinstance(_factory().create(src), CustomApiSource)


def test_create_manual_provider_returns_manual_source():
    src = _source(SourceTypes.WORK_ITEM_QUERY, **{ConfigKeys.PROVIDER: "manual"})
    assert isinstance(_factory().create(src), ManualDocumentSource)


# ── ADO type dispatch ──────────────────────────────────────────────────────────

def test_create_workitem_query_returns_ado_workitem_source():
    src = _source(SourceTypes.WORK_ITEM_QUERY)
    assert isinstance(_factory().create(src), AdoWorkItemQuerySource)


def test_create_documentation_wiki_returns_ado_wiki_source():
    src = _source(SourceTypes.DOCUMENTATION, **{ConfigKeys.DOC_TYPE: "wiki"})
    assert isinstance(_factory().create(src), AdoWikiSource)


def test_create_documentation_upload_returns_manual_source():
    src = _source(SourceTypes.DOCUMENTATION, **{ConfigKeys.DOC_TYPE: "upload"})
    assert isinstance(_factory().create(src), ManualDocumentSource)


def test_create_test_results_returns_ado_test_results_source():
    src = _source(SourceTypes.TEST_RESULTS)
    assert isinstance(_factory().create(src), AdoTestResultsSource)


def test_create_pull_request_returns_ado_pull_request_source():
    src = _source(SourceTypes.PULL_REQUEST)
    assert isinstance(_factory().create(src), AdoPullRequestSource)


def test_create_git_commits_returns_ado_git_commits_source():
    src = _source(SourceTypes.GIT_COMMITS)
    assert isinstance(_factory().create(src), AdoGitCommitsSource)


def test_create_pipeline_build_returns_ado_pipeline_build_source():
    src = _source(SourceTypes.PIPELINE_BUILD)
    assert isinstance(_factory().create(src), AdoPipelineBuildSource)


def test_create_code_repo_returns_ado_code_repo_source():
    src = _source(SourceTypes.CODE_REPO)
    assert isinstance(_factory().create(src), AdoCodeRepoSource)


def test_create_unknown_type_raises_value_error():
    src = _source("totally-unknown-type")
    with pytest.raises(ValueError, match="Unknown source type"):
        _factory().create(src)


# ── collection_for ────────────────────────────────────────────────────────────

@pytest.mark.parametrize("type_,expected", [
    (SourceTypes.WORK_ITEM_QUERY, CollectionNames.WORK_ITEMS),
    (SourceTypes.TEST_CASE,       CollectionNames.TEST_CASES),
    (SourceTypes.TEST_RESULTS,    CollectionNames.TEST_RESULTS),
    (SourceTypes.PULL_REQUEST,    CollectionNames.PULL_REQUESTS),
    (SourceTypes.GIT_COMMITS,     CollectionNames.COMMITS),
    (SourceTypes.CODE_REPO,       CollectionNames.CODE),
    (SourceTypes.PIPELINE_BUILD,  CollectionNames.BUILDS),
    (SourceTypes.DOCUMENTATION,   CollectionNames.DOCUMENTATION),
])
def test_collection_for_known_types(type_, expected):
    assert collection_for(_source(type_)) == expected


def test_collection_for_unknown_type_falls_back_to_work_items():
    assert collection_for(_source("unknown-type")) == CollectionNames.WORK_ITEMS
