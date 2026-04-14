"""
Tests for _build_source_from_form – Git Commits source type.

UI layout: single "Filters" tab (no subcards, no hidden mode key).

  ConfigRepository    required, no default
  ConfigBranch        optional, template pre-fill: "Main" (user can clear it)
  ConfigLastNCommits  template pre-fill: "100", server default: "100"

Server-default leakage for non-ADO providers:
  LastNCommits="100" leaks (server default fires unconditionally).
  Repository and Branch have no defaults → never leak.
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helper ────────────────────────────────────────────────────────────────────

def _gc(**fields) -> dict:
    """Convenience wrapper for git-commits _cfg calls."""
    return _cfg(SourceTypes.GIT_COMMITS, **fields)


# ── Repository field ──────────────────────────────────────────────────────────

def test_gc_repository_stored():
    cfg = _gc(ConfigRepository="MyRepo")
    assert cfg[ConfigKeys.REPOSITORY] == "MyRepo"


def test_gc_repository_absent_when_not_submitted():
    cfg = _gc()
    assert ConfigKeys.REPOSITORY not in cfg


def test_gc_repository_absent_when_empty():
    # Empty string is falsy in _set → not stored, no server default
    cfg = _gc(ConfigRepository="")
    assert ConfigKeys.REPOSITORY not in cfg


def test_gc_repository_preserves_case():
    cfg = _gc(ConfigRepository="MyProject-Repo")
    assert cfg[ConfigKeys.REPOSITORY] == "MyProject-Repo"


# ── Branch field ─────────────────────────────────────────────────────────────

def test_gc_branch_stored():
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="main")
    assert cfg[ConfigKeys.BRANCH] == "main"


def test_gc_branch_absent_when_not_submitted():
    cfg = _gc(ConfigRepository="Repo")
    assert ConfigKeys.BRANCH not in cfg


def test_gc_branch_absent_when_empty():
    # User clears the pre-filled "Main" → empty → not stored
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="")
    assert ConfigKeys.BRANCH not in cfg


def test_gc_branch_template_prefill_main():
    # Template pre-fills "Main"; submitted as-is
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="Main")
    assert cfg[ConfigKeys.BRANCH] == "Main"


def test_gc_branch_feature_branch():
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="feature/my-feature")
    assert cfg[ConfigKeys.BRANCH] == "feature/my-feature"


def test_gc_branch_no_server_default():
    # Branch has no server default → absent when not submitted (not "main" or similar)
    cfg = _gc(ConfigRepository="Repo")
    assert ConfigKeys.BRANCH not in cfg


# ── LastNCommits field ────────────────────────────────────────────────────────

def test_gc_last_n_commits_stored():
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="main",
              ConfigLastNCommits="50")
    assert cfg[ConfigKeys.LAST_N_COMMITS] == "50"


def test_gc_last_n_commits_server_default_when_absent():
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="main")
    assert cfg[ConfigKeys.LAST_N_COMMITS] == "100"


def test_gc_last_n_commits_server_default_when_empty():
    # Empty string is falsy → server default "100" fires
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="main",
              ConfigLastNCommits="")
    assert cfg[ConfigKeys.LAST_N_COMMITS] == "100"


def test_gc_last_n_commits_template_prefill_hundred():
    # Template pre-fills value="100"; submitted as-is
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="main",
              ConfigLastNCommits="100")
    assert cfg[ConfigKeys.LAST_N_COMMITS] == "100"


def test_gc_last_n_commits_min_boundary():
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="main",
              ConfigLastNCommits="1")
    assert cfg[ConfigKeys.LAST_N_COMMITS] == "1"


def test_gc_last_n_commits_max_boundary():
    cfg = _gc(ConfigRepository="Repo", ConfigBranch="main",
              ConfigLastNCommits="2000")
    assert cfg[ConfigKeys.LAST_N_COMMITS] == "2000"


# ── All fields together ───────────────────────────────────────────────────────

def test_gc_all_fields_stored():
    cfg = _gc(ConfigRepository="MyRepo",
              ConfigBranch="release/1.0",
              ConfigLastNCommits="200")
    assert cfg[ConfigKeys.REPOSITORY] == "MyRepo"
    assert cfg[ConfigKeys.BRANCH] == "release/1.0"
    assert cfg[ConfigKeys.LAST_N_COMMITS] == "200"


# ── ADO connection fields ─────────────────────────────────────────────────────

def test_gc_ado_base_url_stored():
    cfg = _gc(ConfigRepository="Repo",
              ConfigBaseUrl="https://dev.azure.com/myorg/myproject")
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_gc_ado_api_version_stored():
    cfg = _gc(ConfigRepository="Repo", ConfigApiVersion="7.1")
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_gc_verify_ssl_not_stored():
    cfg = _gc(ConfigRepository="Repo", ConfigVerifySSL="false")
    assert ConfigKeys.VERIFY_SSL not in cfg  # BUG: VerifySSL is silently dropped


# ── Auth type subcards (ADO connection) ───────────────────────────────────────

def test_gc_auth_pat_stores_pat_only():
    cfg = _gc(ConfigRepository="Repo",
              ConfigAuthType="pat",
              ConfigPat="MY_PAT",
              ConfigToken="SHOULD_BE_IGNORED")
    assert cfg[ConfigKeys.PAT] == "MY_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_gc_auth_bearer_stores_token_only():
    cfg = _gc(ConfigRepository="Repo",
              ConfigAuthType="bearer",
              ConfigPat="SHOULD_BE_IGNORED",
              ConfigToken="MY_BEARER_TOKEN")
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_gc_auth_ntlm_stores_windows_credentials():
    cfg = _gc(ConfigRepository="Repo",
              ConfigAuthType="ntlm",
              ConfigUsername="CORP\\svc",
              ConfigPassword="MY_PASSWORD",
              ConfigDomain="CORP")
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_gc_auth_negotiate_stores_windows_credentials():
    cfg = _gc(ConfigRepository="Repo",
              ConfigAuthType="negotiate",
              ConfigUsername="user",
              ConfigPassword="pass",
              ConfigDomain="DOM")
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_gc_auth_apikey_stores_header_and_value():
    cfg = _gc(ConfigRepository="Repo",
              ConfigAuthType="apikey",
              ConfigApiKeyHeader="X-Api-Key",
              ConfigApiKeyValue="MY_SECRET")
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET"


def test_gc_auth_none_stores_no_credentials():
    cfg = _gc(ConfigRepository="Repo",
              ConfigAuthType="none",
              ConfigPat="ignored", ConfigToken="ignored",
              ConfigUsername="ignored", ConfigPassword="ignored",
              ConfigApiKeyValue="ignored")
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_gc_auth_ntlm_does_not_store_pat():
    cfg = _gc(ConfigRepository="Repo",
              ConfigAuthType="ntlm",
              ConfigPat="SHOULD_BE_IGNORED",
              ConfigUsername="u", ConfigPassword="p", ConfigDomain="d")
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with git-commits source type ─────────────────────
# sysTab('custom') disables ado-provider-content: ConfigRepository and
# ConfigBranch are disabled → not submitted → not stored (no server defaults).
# ConfigLastNCommits is also disabled → server default "100" leaks in.

def test_gc_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/commits",
               ConfigHttpMethod="GET")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/commits"
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_gc_custom_provider_stores_response_mapping():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/commits",
               ConfigItemsPath="value",
               ConfigTitleField="message",
               ConfigContentFields="author,date,message")
    assert cfg[ConfigKeys.ITEMS_PATH] == "value"
    assert cfg[ConfigKeys.TITLE_FIELD] == "message"
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "author,date,message"


def test_gc_custom_provider_last_n_commits_server_default_leaks():
    # ConfigLastNCommits disabled in browser → server defaults "100"
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/commits")
    assert cfg.get(ConfigKeys.LAST_N_COMMITS) == "100"


def test_gc_custom_provider_repository_absent():
    # ConfigRepository has no server default → not stored for custom provider
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/commits")
    assert ConfigKeys.REPOSITORY not in cfg


def test_gc_custom_provider_branch_absent():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/commits")
    assert ConfigKeys.BRANCH not in cfg


def test_gc_custom_provider_no_ado_connection_fields():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/commits")
    assert ConfigKeys.BASE_URL not in cfg
    assert ConfigKeys.API_VERSION not in cfg


# ── Provider tab: Manual with git-commits source type ────────────────────────

def test_gc_manual_provider_text_stores_content_and_title():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Commit History",
               ConfigManualText="feat: add feature\nfix: fix bug")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "feat: add feature\nfix: fix bug"
    assert cfg[ConfigKeys.TITLE] == "Commit History"


def test_gc_manual_provider_upload_carries_existing_content():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualExistingContent="Previously uploaded commit log")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded commit log"


def test_gc_manual_provider_last_n_commits_server_default_leaks():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="commits")
    assert cfg.get(ConfigKeys.LAST_N_COMMITS) == "100"


def test_gc_manual_provider_repository_absent():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="commits")
    assert ConfigKeys.REPOSITORY not in cfg


def test_gc_manual_provider_branch_absent():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="commits")
    assert ConfigKeys.BRANCH not in cfg


def test_gc_manual_provider_no_ado_connection_fields():
    cfg = _cfg(SourceTypes.GIT_COMMITS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="commits")
    assert ConfigKeys.BASE_URL not in cfg
    assert ConfigKeys.API_VERSION not in cfg
