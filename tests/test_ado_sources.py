"""
Tests for all 8 Azure DevOps source implementations.

All tests use AsyncMock for ADO client methods — no live ADO connection required.
"""
from unittest.mock import AsyncMock, MagicMock

import pytest

from app.models import ConfigKeys, SourceDefinition, SourceTypes
from app.sources.ado_build import AdoPipelineBuildSource
from app.sources.ado_code import AdoCodeRepoSource, _glob_matches
from app.sources.ado_commits import AdoGitCommitsSource
from app.sources.ado_pullrequest import AdoPullRequestSource
from app.sources.ado_testcase import AdoTestCaseSource, _strip_xml
from app.sources.ado_testresults import AdoTestResultsSource
from app.sources.ado_wiki import AdoWikiSource
from app.sources.ado_workitem import AdoWorkItemQuerySource, _build_wiql, _strip_html


# ── Shared helpers ─────────────────────────────────────────────────────────────

def _client() -> MagicMock:
    c = MagicMock()
    c.run_work_item_query = AsyncMock(return_value=[])
    c.get_builds = AsyncMock(return_value=[])
    c.get_build_timeline = AsyncMock(return_value=[])
    c.get_pull_requests = AsyncMock(return_value=[])
    c.get_test_runs = AsyncMock(return_value=[])
    c.get_test_results = AsyncMock(return_value=[])
    c.get_commits = AsyncMock(return_value=[])
    c.get_wikis = AsyncMock(return_value=[])
    c.get_wiki_items = AsyncMock(return_value=[])
    c.get_file_tree = AsyncMock(return_value=[])
    c.get_file_content = AsyncMock(return_value="")
    return c


def _source(type_: str = SourceTypes.WORK_ITEM_QUERY, **config) -> SourceDefinition:
    base = {"BaseUrl": "https://dev.azure.com/org/proj", "AuthType": "none"}
    base.update(config)
    return SourceDefinition(type=type_, name="Test Source", config=base)


def _wi(id_: int, title: str = "My Work Item", state: str = "Active", wi_type: str = "Bug") -> dict:
    return {
        "id": id_,
        "fields": {
            "System.Title": title,
            "System.State": state,
            "System.WorkItemType": wi_type,
        },
    }


# ── _build_wiql (pure unit tests) ─────────────────────────────────────────────

def test_build_wiql_with_types_generates_in_clause():
    wiql = _build_wiql(["Bug", "Task"], "")
    assert "[System.WorkItemType] IN ('Bug', 'Task')" in wiql


def test_build_wiql_with_area_path_generates_under_clause():
    wiql = _build_wiql([], "MyProject\\Team")
    assert "[System.AreaPath] UNDER 'MyProject\\Team'" in wiql


def test_build_wiql_with_types_and_area_uses_and():
    wiql = _build_wiql(["Bug"], "MyProject")
    assert " AND " in wiql


def test_build_wiql_empty_conditions_falls_back_to_project():
    wiql = _build_wiql([], "")
    assert "[System.TeamProject] = @project" in wiql


def test_build_wiql_always_has_order_clause():
    wiql = _build_wiql(["Bug"], "")
    assert "ORDER BY [System.ChangedDate] DESC" in wiql


# ── _strip_xml (pure unit test) ───────────────────────────────────────────────

def test_strip_xml_removes_tags():
    assert _strip_xml("<div>Hello <b>world</b></div>") == "Hello  world"


def test_strip_xml_empty_string():
    assert _strip_xml("") == ""


def test_strip_xml_no_tags_returns_stripped():
    assert _strip_xml("  plain text  ") == "plain text"


# ── _strip_html (pure unit tests) ─────────────────────────────────────────────

def test_strip_html_removes_simple_tags():
    assert _strip_html("<div>Hello <b>world</b></div>") == "Hello world"


def test_strip_html_removes_html_entities():
    assert _strip_html("Hello&nbsp;world") == "Hello world"


def test_strip_html_collapses_whitespace():
    assert _strip_html("<p>  lots   of   space  </p>") == "lots of space"


def test_strip_html_empty_string():
    assert _strip_html("") == ""


def test_strip_html_plain_text_unchanged():
    assert _strip_html("plain text") == "plain text"


def test_strip_html_self_closing_tags():
    assert _strip_html("line1<br/>line2") == "line1 line2"


def test_strip_html_nested_tags():
    result = _strip_html("<div><p>Acceptance <strong>criteria</strong></p></div>")
    assert "<" not in result
    assert "Acceptance" in result
    assert "criteria" in result


def test_strip_html_multiple_entities():
    result = _strip_html("A&nbsp;&amp;B")
    assert "<" not in result
    assert "&" not in result


# ── _glob_matches (pure unit tests) ───────────────────────────────────────────

def test_glob_matches_exact_extension():
    assert _glob_matches("src/foo.cs", ["**/*.cs"])


def test_glob_matches_txt_excluded_by_cs_pattern():
    assert not _glob_matches("src/foo.txt", ["**/*.cs"])


def test_glob_matches_multiple_patterns():
    assert _glob_matches("src/foo.py", ["**/*.cs", "**/*.py"])


def test_glob_matches_leading_slash_normalised():
    assert _glob_matches("/src/foo.cs", ["**/*.cs"])


def test_glob_matches_no_patterns_returns_false():
    assert not _glob_matches("src/foo.cs", [])


def test_glob_matches_root_file():
    assert _glob_matches("main.cs", ["**/*.cs"])


# ── AdoWorkItemQuerySource ────────────────────────────────────────────────────

async def test_workitem_returns_correct_doc_count():
    client = _client()
    client.run_work_item_query = AsyncMock(return_value=[_wi(1), _wi(2)])
    docs = await AdoWorkItemQuerySource(_source(), client).fetch_documents()
    assert len(docs) == 2


async def test_workitem_doc_id_uses_wi_pattern():
    client = _client()
    client.run_work_item_query = AsyncMock(return_value=[_wi(42)])
    src = _source()
    docs = await AdoWorkItemQuerySource(src, client).fetch_documents()
    assert docs[0].id == f"{src.id}_wi_42"


async def test_workitem_text_starts_with_work_item_prefix():
    client = _client()
    client.run_work_item_query = AsyncMock(return_value=[_wi(7, title="Broken login")])
    docs = await AdoWorkItemQuerySource(_source(), client).fetch_documents()
    assert docs[0].text.startswith("Work Item 7: Broken login")


async def test_workitem_tags_contain_type_and_state():
    client = _client()
    client.run_work_item_query = AsyncMock(return_value=[_wi(1, wi_type="Feature", state="Closed")])
    docs = await AdoWorkItemQuerySource(_source(), client).fetch_documents()
    assert docs[0].tags["work_item_type"] == "Feature"
    assert docs[0].tags["state"] == "Closed"


async def test_workitem_properties_url_contains_base_url():
    client = _client()
    client.run_work_item_query = AsyncMock(return_value=[_wi(5)])
    docs = await AdoWorkItemQuerySource(_source(BaseUrl="https://ado.example.com/proj"), client).fetch_documents()
    assert "https://ado.example.com/proj" in docs[0].properties["url"]


async def test_workitem_empty_response_returns_empty_list():
    docs = await AdoWorkItemQuerySource(_source(), _client()).fetch_documents()
    assert docs == []


async def test_workitem_custom_wiql_used_when_set():
    client = _client()
    src = _source(**{ConfigKeys.QUERY: "SELECT [System.Id] FROM WorkItems WHERE [System.State] = 'New'"})
    await AdoWorkItemQuerySource(src, client).fetch_documents()
    called_wiql = client.run_work_item_query.call_args[0][1]
    assert "WHERE [System.State] = 'New'" in called_wiql


async def test_workitem_default_wiql_used_when_no_query():
    client = _client()
    await AdoWorkItemQuerySource(_source(), client).fetch_documents()
    called_wiql = client.run_work_item_query.call_args[0][1]
    assert "WorkItems" in called_wiql


async def test_workitem_item_types_config_parsed_into_wiql():
    client = _client()
    src = _source(**{ConfigKeys.ITEM_TYPES: "Bug, Task, Feature"})
    await AdoWorkItemQuerySource(src, client).fetch_documents()
    called_wiql = client.run_work_item_query.call_args[0][1]
    assert "'Bug'" in called_wiql
    assert "'Task'" in called_wiql
    assert "'Feature'" in called_wiql


async def test_workitem_area_path_config_used_in_wiql():
    client = _client()
    src = _source(**{ConfigKeys.AREA_PATH: "MyProject\\MyTeam"})
    await AdoWorkItemQuerySource(src, client).fetch_documents()
    called_wiql = client.run_work_item_query.call_args[0][1]
    assert "MyProject\\MyTeam" in called_wiql


async def test_workitem_fields_config_passed_to_client():
    client = _client()
    src = _source(**{ConfigKeys.FIELDS: "System.Title, System.State"})
    await AdoWorkItemQuerySource(src, client).fetch_documents()
    called_fields = client.run_work_item_query.call_args[0][2]
    assert "System.Title" in called_fields
    assert "System.State" in called_fields


async def test_workitem_empty_fields_config_passes_empty_list():
    client = _client()
    await AdoWorkItemQuerySource(_source(), client).fetch_documents()
    called_fields = client.run_work_item_query.call_args[0][2]
    assert called_fields == []


async def test_workitem_html_stripped_from_field_values():
    client = _client()
    item = {
        "id": 99,
        "fields": {
            "System.Title": "Bug: login fails",
            "System.State": "Active",
            "System.WorkItemType": "Bug",
            "System.Description": "<div><p>Steps to reproduce:<br/>1. Open app&nbsp;2. Click login</p></div>",
            "Microsoft.VSTS.Common.AcceptanceCriteria": "<ul><li>User can log in</li><li>Token is valid</li></ul>",
        },
    }
    client.run_work_item_query = AsyncMock(return_value=[item])
    docs = await AdoWorkItemQuerySource(_source(), client).fetch_documents()
    assert len(docs) == 1
    assert "<" not in docs[0].text
    assert "Steps to reproduce" in docs[0].text
    assert "User can log in" in docs[0].text


async def test_workitem_html_entity_stripped_from_field_values():
    client = _client()
    item = {
        "id": 5,
        "fields": {
            "System.Title": "Task",
            "System.State": "New",
            "System.WorkItemType": "Task",
            "System.Description": "Do A&nbsp;and B&lt;C",
        },
    }
    client.run_work_item_query = AsyncMock(return_value=[item])
    docs = await AdoWorkItemQuerySource(_source(), client).fetch_documents()
    assert "&nbsp;" not in docs[0].text
    assert "&lt;" not in docs[0].text


# ── AdoTestCaseSource ─────────────────────────────────────────────────────────

def _tc(id_: int, title: str = "TC Title", state: str = "Ready",
         auto: str = "Automated", steps_xml: str = "") -> dict:
    return {
        "id": id_,
        "fields": {
            "System.Title": title,
            "System.State": state,
            "Microsoft.VSTS.TCM.AutomationStatus": auto,
            "Microsoft.VSTS.TCM.Steps": steps_xml,
        },
    }


async def test_testcase_doc_id_uses_tc_pattern():
    client = _client()
    client.run_work_item_query = AsyncMock(return_value=[_tc(10)])
    src = _source()
    docs = await AdoTestCaseSource(src, client).fetch_documents()
    assert docs[0].id == f"{src.id}_tc_10"


async def test_testcase_xml_stripped_from_steps():
    client = _client()
    client.run_work_item_query = AsyncMock(
        return_value=[_tc(1, steps_xml="<steps><step>Click button</step></steps>")]
    )
    docs = await AdoTestCaseSource(_source(), client).fetch_documents()
    assert "<" not in docs[0].text
    assert "Click button" in docs[0].text


async def test_testcase_automation_status_in_tags():
    client = _client()
    client.run_work_item_query = AsyncMock(return_value=[_tc(1, auto="Not Automated")])
    docs = await AdoTestCaseSource(_source(), client).fetch_documents()
    assert docs[0].tags["automation_status"] == "Not Automated"


async def test_testcase_default_wiql_used_when_no_query():
    client = _client()
    await AdoTestCaseSource(_source(), client).fetch_documents()
    called_wiql = client.run_work_item_query.call_args[0][1]
    assert "Test Case" in called_wiql


async def test_testcase_custom_wiql_overrides_default():
    client = _client()
    custom = "SELECT [System.Id] FROM WorkItems WHERE [System.Title] CONTAINS 'Login'"
    src = _source(**{ConfigKeys.QUERY: custom})
    await AdoTestCaseSource(src, client).fetch_documents()
    called_wiql = client.run_work_item_query.call_args[0][1]
    assert "Login" in called_wiql


# ── AdoPipelineBuildSource ────────────────────────────────────────────────────

def _build(id_: int, number: str = "20240101.1", result: str = "succeeded",
           status: str = "completed") -> dict:
    return {
        "id": id_,
        "buildNumber": number,
        "result": result,
        "status": status,
        "finishTime": "2024-01-01T00:00:00Z",
        "_links": {"web": {"href": f"https://ado.example.com/_build/results?buildId={id_}"}},
    }


async def test_build_doc_id_uses_build_pattern():
    client = _client()
    client.get_builds = AsyncMock(return_value=[_build(99)])
    src = _source()
    docs = await AdoPipelineBuildSource(src, client).fetch_documents()
    assert docs[0].id == f"{src.id}_build_99"


async def test_build_tags_contain_pipeline_id_and_result():
    client = _client()
    client.get_builds = AsyncMock(return_value=[_build(1, result="failed")])
    src = _source(**{ConfigKeys.PIPELINE_ID: "42"})
    docs = await AdoPipelineBuildSource(src, client).fetch_documents()
    assert docs[0].tags["pipeline_id"] == "42"
    assert docs[0].tags["build_result"] == "failed"


async def test_build_failed_triggers_timeline_fetch():
    client = _client()
    client.get_builds = AsyncMock(return_value=[_build(1, result="failed")])
    client.get_build_timeline = AsyncMock(return_value=[])
    await AdoPipelineBuildSource(_source(), client).fetch_documents()
    client.get_build_timeline.assert_called_once()


async def test_build_succeeded_skips_timeline_fetch():
    client = _client()
    client.get_builds = AsyncMock(return_value=[_build(1, result="succeeded")])
    await AdoPipelineBuildSource(_source(), client).fetch_documents()
    client.get_build_timeline.assert_not_called()


async def test_build_empty_response_returns_empty_list():
    docs = await AdoPipelineBuildSource(_source(), _client()).fetch_documents()
    assert docs == []


async def test_build_default_last_n_is_5():
    client = _client()
    await AdoPipelineBuildSource(_source(), client).fetch_documents()
    _, _, last_n = client.get_builds.call_args[0]
    assert last_n == 5


async def test_build_default_pipeline_id_is_0():
    client = _client()
    await AdoPipelineBuildSource(_source(), client).fetch_documents()
    _, pipeline_id, _ = client.get_builds.call_args[0]
    assert pipeline_id == 0


async def test_build_partially_succeeded_also_triggers_timeline_fetch():
    client = _client()
    client.get_builds = AsyncMock(return_value=[_build(1, result="partiallySucceeded")])
    client.get_build_timeline = AsyncMock(return_value=[])
    await AdoPipelineBuildSource(_source(), client).fetch_documents()
    client.get_build_timeline.assert_called_once()


async def test_build_failed_tasks_appear_in_text():
    client = _client()
    client.get_builds = AsyncMock(return_value=[_build(1, result="failed")])
    client.get_build_timeline = AsyncMock(return_value=[
        {"result": "failed", "type": "Task", "name": "RunTests", "issues": "timeout"}
    ])
    docs = await AdoPipelineBuildSource(_source(), client).fetch_documents()
    assert "RunTests" in docs[0].text


# ── AdoPullRequestSource ──────────────────────────────────────────────────────

def _pr(id_: int, title: str = "PR Title", status: str = "active",
        source_branch: str = "refs/heads/feature/my-branch",
        target_branch: str = "refs/heads/main",
        author: str = "Alice") -> dict:
    return {
        "pullRequestId": id_,
        "title": title,
        "status": status,
        "sourceRefName": source_branch,
        "targetRefName": target_branch,
        "createdBy": {"displayName": author},
        "reviewers": [],
        "description": "",
        "creationDate": "2024-01-01T00:00:00Z",
        "closedDate": None,
    }


async def test_pr_doc_id_uses_pr_pattern():
    client = _client()
    client.get_pull_requests = AsyncMock(return_value=[_pr(55)])
    src = _source()
    docs = await AdoPullRequestSource(src, client).fetch_documents()
    assert docs[0].id == f"{src.id}_pr_55"


async def test_pr_refs_heads_stripped_from_branches():
    client = _client()
    client.get_pull_requests = AsyncMock(return_value=[_pr(1)])
    docs = await AdoPullRequestSource(_source(), client).fetch_documents()
    assert "refs/heads/" not in docs[0].text
    assert "feature/my-branch" in docs[0].text


async def test_pr_author_in_tags():
    client = _client()
    client.get_pull_requests = AsyncMock(return_value=[_pr(1, author="Bob")])
    docs = await AdoPullRequestSource(_source(), client).fetch_documents()
    assert docs[0].tags["author"] == "Bob"


async def test_pr_status_in_tags():
    client = _client()
    client.get_pull_requests = AsyncMock(return_value=[_pr(1, status="completed")])
    docs = await AdoPullRequestSource(_source(), client).fetch_documents()
    assert docs[0].tags["status"] == "completed"


async def test_pr_empty_response_returns_empty_list():
    docs = await AdoPullRequestSource(_source(), _client()).fetch_documents()
    assert docs == []


async def test_pr_default_status_filter_is_all():
    client = _client()
    await AdoPullRequestSource(_source(), client).fetch_documents()
    _, _, status, _ = client.get_pull_requests.call_args[0]
    assert status == "all"


async def test_pr_default_top_is_200():
    client = _client()
    await AdoPullRequestSource(_source(), client).fetch_documents()
    _, _, _, top = client.get_pull_requests.call_args[0]
    assert top == 200


async def test_pr_properties_url_uses_repo_and_id():
    client = _client()
    client.get_pull_requests = AsyncMock(return_value=[_pr(7)])
    src = _source(**{ConfigKeys.REPOSITORY: "MyRepo", "BaseUrl": "https://ado.example.com/proj"})
    docs = await AdoPullRequestSource(src, client).fetch_documents()
    assert "MyRepo" in docs[0].properties["url"]
    assert "7" in docs[0].properties["url"]


# ── AdoTestResultsSource ──────────────────────────────────────────────────────

def _run(id_: int, name: str = "Nightly Run", state: str = "Completed") -> dict:
    return {"id": id_, "name": name, "state": state, "completedDate": "2024-01-01T00:00:00Z"}


def _result(id_: int, test_name: str = "Login Test", outcome: str = "Passed",
             error: str = "") -> dict:
    return {
        "id": id_,
        "testCaseTitle": test_name,
        "testCase": {"name": test_name},
        "outcome": outcome,
        "errorMessage": error,
        "stackTrace": "",
        "durationInMs": 100,
        "priority": 2,
    }


async def test_testresults_doc_id_uses_tr_pattern():
    client = _client()
    client.get_test_runs = AsyncMock(return_value=[_run(10)])
    client.get_test_results = AsyncMock(return_value=[_result(20)])
    src = _source()
    docs = await AdoTestResultsSource(src, client).fetch_documents()
    assert docs[0].id == f"{src.id}_tr_10_20"


async def test_testresults_two_client_calls_per_run():
    client = _client()
    client.get_test_runs = AsyncMock(return_value=[_run(1), _run(2)])
    client.get_test_results = AsyncMock(return_value=[_result(1)])
    await AdoTestResultsSource(_source(), client).fetch_documents()
    assert client.get_test_results.call_count == 2


async def test_testresults_outcome_in_tags():
    client = _client()
    client.get_test_runs = AsyncMock(return_value=[_run(1)])
    client.get_test_results = AsyncMock(return_value=[_result(1, outcome="Failed")])
    docs = await AdoTestResultsSource(_source(), client).fetch_documents()
    assert docs[0].tags["outcome"] == "Failed"


async def test_testresults_run_name_in_tags():
    client = _client()
    client.get_test_runs = AsyncMock(return_value=[_run(1, name="Smoke Tests")])
    client.get_test_results = AsyncMock(return_value=[_result(1)])
    docs = await AdoTestResultsSource(_source(), client).fetch_documents()
    assert docs[0].tags["run_name"] == "Smoke Tests"


async def test_testresults_error_message_in_text():
    client = _client()
    client.get_test_runs = AsyncMock(return_value=[_run(1)])
    client.get_test_results = AsyncMock(return_value=[_result(1, error="NullPointerException")])
    docs = await AdoTestResultsSource(_source(), client).fetch_documents()
    assert "NullPointerException" in docs[0].text


async def test_testresults_empty_runs_returns_empty_list():
    docs = await AdoTestResultsSource(_source(), _client()).fetch_documents()
    assert docs == []


async def test_testresults_default_last_n_runs_is_10():
    client = _client()
    await AdoTestResultsSource(_source(), client).fetch_documents()
    _, last_n = client.get_test_runs.call_args[0]
    assert last_n == 10


async def test_testresults_default_results_per_run_is_200():
    client = _client()
    client.get_test_runs = AsyncMock(return_value=[_run(1)])
    client.get_test_results = AsyncMock(return_value=[])
    await AdoTestResultsSource(_source(), client).fetch_documents()
    _, _, per_run = client.get_test_results.call_args[0]
    assert per_run == 200


# ── AdoGitCommitsSource ───────────────────────────────────────────────────────

def _commit(commit_id: str = "abcdef1234567890", message: str = "Fix bug",
             author: str = "Alice", repo: str = "MyRepo") -> dict:
    return {
        "commitId": commit_id,
        "comment": message,
        "author": {"name": author, "email": "alice@example.com", "date": "2024-01-01T00:00:00Z"},
        "changeCounts": {"Add": 2, "Edit": 3, "Delete": 1},
    }


async def test_commits_doc_id_uses_short_id():
    client = _client()
    client.get_commits = AsyncMock(return_value=[_commit("abcdef1234567890")])
    src = _source()
    docs = await AdoGitCommitsSource(src, client).fetch_documents()
    assert docs[0].id == f"{src.id}_commit_abcdef12"


async def test_commits_short_id_is_first_8_chars():
    commit_id = "1234567890abcdef"
    client = _client()
    client.get_commits = AsyncMock(return_value=[_commit(commit_id)])
    docs = await AdoGitCommitsSource(_source(), client).fetch_documents()
    assert "12345678" in docs[0].id


async def test_commits_author_in_tags():
    client = _client()
    client.get_commits = AsyncMock(return_value=[_commit(author="Bob")])
    docs = await AdoGitCommitsSource(_source(), client).fetch_documents()
    assert docs[0].tags["author"] == "Bob"


async def test_commits_repository_in_tags():
    client = _client()
    client.get_commits = AsyncMock(return_value=[_commit()])
    src = _source(**{ConfigKeys.REPOSITORY: "CoreRepo"})
    docs = await AdoGitCommitsSource(src, client).fetch_documents()
    assert docs[0].tags["repository"] == "CoreRepo"


async def test_commits_change_counts_in_text():
    client = _client()
    client.get_commits = AsyncMock(return_value=[_commit()])
    docs = await AdoGitCommitsSource(_source(), client).fetch_documents()
    assert "Changes:" in docs[0].text


async def test_commits_empty_response_returns_empty_list():
    docs = await AdoGitCommitsSource(_source(), _client()).fetch_documents()
    assert docs == []


async def test_commits_default_branch_is_main():
    client = _client()
    await AdoGitCommitsSource(_source(), client).fetch_documents()
    _, _, branch, _ = client.get_commits.call_args[0]
    assert branch == "main"


async def test_commits_default_top_is_100():
    client = _client()
    await AdoGitCommitsSource(_source(), client).fetch_documents()
    _, _, _, top = client.get_commits.call_args[0]
    assert top == 100


async def test_commits_no_change_counts_omits_changes_line():
    client = _client()
    commit = {
        "commitId": "aabbccdd11223344",
        "comment": "fix typo",
        "author": {"name": "Alice", "email": "a@x.com", "date": "2024-01-01T00:00:00Z"},
        # changeCounts absent entirely
    }
    client.get_commits = AsyncMock(return_value=[commit])
    docs = await AdoGitCommitsSource(_source(), client).fetch_documents()
    assert "Changes:" not in docs[0].text


# ── AdoWikiSource ─────────────────────────────────────────────────────────────

def _wiki(id_: str = "wiki-1", name: str = "ProjectWiki") -> dict:
    return {"id": id_, "name": name}


def _page(path: str = "/Home", content: str = "# Home\nSome content.") -> dict:
    return {"path": path, "content": content, "subPages": []}


async def test_wiki_returns_empty_list_when_no_wikis():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[])
    docs = await AdoWikiSource(_source(), client).fetch_documents()
    assert docs == []


async def test_wiki_get_wikis_called_first():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki()])
    client.get_wiki_items = AsyncMock(return_value=[])
    await AdoWikiSource(_source(), client).fetch_documents()
    client.get_wikis.assert_called_once()


async def test_wiki_uses_first_wiki_when_no_name_configured():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki("w1", "First"), _wiki("w2", "Second")])
    client.get_wiki_items = AsyncMock(return_value=[])
    await AdoWikiSource(_source(), client).fetch_documents()
    called_wiki_id = client.get_wiki_items.call_args[0][1]
    assert called_wiki_id == "w1"


async def test_wiki_selects_wiki_by_name():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki("w1", "First"), _wiki("w2", "Target")])
    client.get_wiki_items = AsyncMock(return_value=[])
    src = _source(**{ConfigKeys.WIKI_NAME: "Target"})
    await AdoWikiSource(src, client).fetch_documents()
    called_wiki_id = client.get_wiki_items.call_args[0][1]
    assert called_wiki_id == "w2"


async def test_wiki_subpages_are_recursed():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki()])
    subpage = _page("/Home/Sub", "# Sub\nSub content.")
    root_page = {**_page("/Home"), "subPages": [subpage]}
    client.get_wiki_items = AsyncMock(return_value=[root_page])
    docs = await AdoWikiSource(_source(), client).fetch_documents()
    paths = [d.properties.get("path") for d in docs]
    assert "/Home/Sub" in paths


async def test_wiki_path_filter_config_passed_to_get_wiki_items():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki()])
    client.get_wiki_items = AsyncMock(return_value=[])
    src = _source(**{ConfigKeys.PATH_FILTER: "/Engineering"})
    await AdoWikiSource(src, client).fetch_documents()
    _, _, path = client.get_wiki_items.call_args[0]
    assert path == "/Engineering"


async def test_wiki_default_path_filter_is_root():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki()])
    client.get_wiki_items = AsyncMock(return_value=[])
    await AdoWikiSource(_source(), client).fetch_documents()
    _, _, path = client.get_wiki_items.call_args[0]
    assert path == "/"


async def test_wiki_configured_name_not_found_falls_back_to_first():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki("w1", "Alpha"), _wiki("w2", "Beta")])
    client.get_wiki_items = AsyncMock(return_value=[])
    src = _source(**{ConfigKeys.WIKI_NAME: "NonExistent"})
    await AdoWikiSource(src, client).fetch_documents()
    called_wiki_id = client.get_wiki_items.call_args[0][1]
    assert called_wiki_id == "w1"


async def test_wiki_page_with_no_content_produces_no_doc():
    client = _client()
    client.get_wikis = AsyncMock(return_value=[_wiki()])
    client.get_wiki_items = AsyncMock(return_value=[{"path": "/Empty", "content": "", "subPages": []}])
    docs = await AdoWikiSource(_source(), client).fetch_documents()
    assert docs == []


# ── AdoCodeRepoSource ─────────────────────────────────────────────────────────

def _file_info(path: str) -> dict:
    return {"path": path, "isFolder": False}


def _parser_registry(units=None):
    unit = MagicMock()
    unit.to_id_slug = MagicMock(return_value="slug-abc")
    unit.enriched_text = "enriched text"
    unit.language = "csharp"
    unit.kind = MagicMock()
    unit.kind.value = "function"
    unit.name = "MyFunction"
    registry = MagicMock()
    registry.parse = MagicMock(return_value=units if units is not None else [unit])
    return registry


async def test_code_file_tree_filtered_by_glob():
    client = _client()
    client.get_file_tree = AsyncMock(return_value=[
        _file_info("/src/Foo.cs"),
        _file_info("/src/Bar.txt"),
    ])
    client.get_file_content = AsyncMock(return_value="class Foo {}")
    registry = _parser_registry()
    src = _source(**{ConfigKeys.GLOB_PATTERNS: "**/*.cs"})
    await AdoCodeRepoSource(src, client, registry).fetch_documents()
    # Only Foo.cs should have been fetched (1 call)
    assert client.get_file_content.call_count == 1


async def test_code_parser_called_per_matched_file():
    client = _client()
    client.get_file_tree = AsyncMock(return_value=[_file_info("/a.cs"), _file_info("/b.cs")])
    client.get_file_content = AsyncMock(return_value="code")
    registry = _parser_registry()
    src = _source(**{ConfigKeys.GLOB_PATTERNS: "**/*.cs"})
    await AdoCodeRepoSource(src, client, registry).fetch_documents()
    assert registry.parse.call_count == 2


async def test_code_failed_file_fetch_is_skipped_gracefully():
    client = _client()
    client.get_file_tree = AsyncMock(return_value=[_file_info("/broken.cs"), _file_info("/ok.cs")])
    client.get_file_content = AsyncMock(side_effect=[Exception("network error"), "class Ok {}"])
    registry = _parser_registry()
    src = _source(**{ConfigKeys.GLOB_PATTERNS: "**/*.cs"})
    # Should not raise
    docs = await AdoCodeRepoSource(src, client, registry).fetch_documents()
    assert registry.parse.call_count == 1


async def test_code_doc_tags_contain_language_and_kind():
    client = _client()
    client.get_file_tree = AsyncMock(return_value=[_file_info("/Foo.cs")])
    client.get_file_content = AsyncMock(return_value="class Foo {}")
    registry = _parser_registry()
    src = _source(**{ConfigKeys.GLOB_PATTERNS: "**/*.cs"})
    docs = await AdoCodeRepoSource(src, client, registry).fetch_documents()
    assert docs[0].tags["language"] == "csharp"
    assert docs[0].tags["kind"] == "function"


async def test_code_empty_tree_returns_empty_list():
    docs = await AdoCodeRepoSource(_source(), _client(), _parser_registry(units=[])).fetch_documents()
    assert docs == []


async def test_code_default_glob_pattern_matches_cs_files():
    client = _client()
    client.get_file_tree = AsyncMock(return_value=[
        _file_info("/src/Foo.cs"),
        _file_info("/src/Bar.py"),
    ])
    client.get_file_content = AsyncMock(return_value="code")
    registry = _parser_registry()
    # No GlobPatterns configured → default is "**/*.cs"
    await AdoCodeRepoSource(_source(), client, registry).fetch_documents()
    assert client.get_file_content.call_count == 1
    fetched_path = client.get_file_content.call_args[0][3]
    assert fetched_path.endswith(".cs")


async def test_code_default_branch_is_main():
    client = _client()
    client.get_file_tree = AsyncMock(return_value=[])
    # No Branch configured → default is "main"
    await AdoCodeRepoSource(_source(), client, _parser_registry(units=[])).fetch_documents()
    _, _, branch = client.get_file_tree.call_args[0]
    assert branch == "main"
