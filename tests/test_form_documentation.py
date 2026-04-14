"""
Tests for _build_source_from_form – Documentation source type.

UI layout: two ADO subcards controlled by docTab().

  [Wiki tab]       ConfigWikiName (optional), ConfigPathFilter (optional)
  [Repo Files tab] ConfigRepository, ConfigBranch (optional),
                   ConfigGlobPatterns (server default "**/*.md")

ConfigDocType is a hidden field (always submitted); docTab() updates its value
and disables inactive panel inputs.

Server defaults:  DocType="wiki" (when ConfigDocType absent)
                  GlobPatterns="**/*.md" (when Repo tab active but field empty)

Cross-provider leakage: DocType="wiki" server default fires for custom/manual
providers (type block runs unconditionally). Since wiki branch does NOT set
GlobPatterns, there is no GlobPatterns leakage for non-ADO providers.
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helpers ───────────────────────────────────────────────────────────────────

def _doc_wiki(**fields) -> dict:
    """Simulates Wiki tab active: ConfigDocType=wiki, repo panel disabled."""
    return _cfg(SourceTypes.DOCUMENTATION, ConfigDocType="wiki", **fields)


def _doc_repo(**fields) -> dict:
    """Simulates Repo Files tab active: ConfigDocType=repo, wiki panel disabled."""
    return _cfg(SourceTypes.DOCUMENTATION, ConfigDocType="repo", **fields)


# ── ConfigDocType hidden field ────────────────────────────────────────────────

def test_doc_type_defaults_to_wiki_when_missing():
    cfg = _cfg(SourceTypes.DOCUMENTATION)
    assert cfg[ConfigKeys.DOC_TYPE] == "wiki"


def test_doc_type_explicit_wiki_stored():
    cfg = _doc_wiki()
    assert cfg[ConfigKeys.DOC_TYPE] == "wiki"


def test_doc_type_repo_stored():
    cfg = _doc_repo(ConfigRepository="Docs")
    assert cfg[ConfigKeys.DOC_TYPE] == "repo"


# ── Wiki subcard ──────────────────────────────────────────────────────────────

def test_doc_wiki_name_stored():
    cfg = _doc_wiki(ConfigWikiName="MyProject.wiki")
    assert cfg[ConfigKeys.WIKI_NAME] == "MyProject.wiki"


def test_doc_wiki_name_absent_when_not_submitted():
    cfg = _doc_wiki()
    assert ConfigKeys.WIKI_NAME not in cfg


def test_doc_wiki_name_absent_when_empty():
    cfg = _doc_wiki(ConfigWikiName="")
    assert ConfigKeys.WIKI_NAME not in cfg


def test_doc_wiki_path_filter_stored():
    cfg = _doc_wiki(ConfigWikiName="Wiki", ConfigPathFilter="/Architecture")
    assert cfg[ConfigKeys.PATH_FILTER] == "/Architecture"


def test_doc_wiki_path_filter_absent_when_not_submitted():
    cfg = _doc_wiki(ConfigWikiName="Wiki")
    assert ConfigKeys.PATH_FILTER not in cfg


def test_doc_wiki_path_filter_absent_when_empty():
    cfg = _doc_wiki(ConfigWikiName="Wiki", ConfigPathFilter="")
    assert ConfigKeys.PATH_FILTER not in cfg


def test_doc_wiki_both_fields_stored():
    cfg = _doc_wiki(ConfigWikiName="MyProject.wiki", ConfigPathFilter="/Architecture")
    assert cfg[ConfigKeys.WIKI_NAME] == "MyProject.wiki"
    assert cfg[ConfigKeys.PATH_FILTER] == "/Architecture"


def test_doc_wiki_neither_field_stored():
    cfg = _doc_wiki()
    assert ConfigKeys.WIKI_NAME not in cfg
    assert ConfigKeys.PATH_FILTER not in cfg


def test_doc_wiki_does_not_store_repository():
    # docTab disables repo panel → Repository not submitted
    cfg = _doc_wiki(ConfigWikiName="Wiki")
    assert ConfigKeys.REPOSITORY not in cfg


def test_doc_wiki_does_not_store_branch():
    cfg = _doc_wiki(ConfigWikiName="Wiki")
    assert ConfigKeys.BRANCH not in cfg


def test_doc_wiki_does_not_store_glob_patterns():
    # Parser only sets GlobPatterns when DocType==repo; wiki branch never touches it
    cfg = _doc_wiki(ConfigWikiName="Wiki")
    assert ConfigKeys.GLOB_PATTERNS not in cfg


def test_doc_wiki_repo_fields_not_stored_even_if_submitted():
    # Even if browser submits repo-panel fields (e.g. during edit round-trip),
    # parser ignores them when DocType==wiki
    cfg = _doc_wiki(ConfigWikiName="Wiki",
                    ConfigRepository="ShouldBeIgnored",
                    ConfigBranch="ShouldBeIgnored",
                    ConfigGlobPatterns="ShouldBeIgnored")
    assert ConfigKeys.REPOSITORY not in cfg
    assert ConfigKeys.BRANCH not in cfg
    assert ConfigKeys.GLOB_PATTERNS not in cfg


# ── Repo Files subcard ────────────────────────────────────────────────────────

def test_doc_repo_repository_stored():
    cfg = _doc_repo(ConfigRepository="DocsRepo")
    assert cfg[ConfigKeys.REPOSITORY] == "DocsRepo"


def test_doc_repo_repository_absent_when_empty():
    cfg = _doc_repo(ConfigRepository="")
    assert ConfigKeys.REPOSITORY not in cfg


def test_doc_repo_branch_stored():
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main")
    assert cfg[ConfigKeys.BRANCH] == "main"


def test_doc_repo_branch_absent_when_not_submitted():
    cfg = _doc_repo(ConfigRepository="Docs")
    assert ConfigKeys.BRANCH not in cfg


def test_doc_repo_branch_absent_when_empty():
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="")
    assert ConfigKeys.BRANCH not in cfg


def test_doc_repo_glob_patterns_stored():
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main",
                    ConfigGlobPatterns="docs/**/*.md")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "docs/**/*.md"


def test_doc_repo_glob_patterns_server_default_when_absent():
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.md"


def test_doc_repo_glob_patterns_template_value_md_only():
    # Template pre-fills "**/*.md" which the user may submit as-is
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main",
                    ConfigGlobPatterns="**/*.md")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.md"


def test_doc_repo_glob_patterns_custom_multi():
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main",
                    ConfigGlobPatterns="**/*.md,**/*.rst")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.md,**/*.rst"


def test_doc_repo_all_fields_stored():
    cfg = _doc_repo(ConfigRepository="DocsRepo", ConfigBranch="release/2.0",
                    ConfigGlobPatterns="specs/**/*.md")
    assert cfg[ConfigKeys.DOC_TYPE] == "repo"
    assert cfg[ConfigKeys.REPOSITORY] == "DocsRepo"
    assert cfg[ConfigKeys.BRANCH] == "release/2.0"
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "specs/**/*.md"


def test_doc_repo_does_not_store_wiki_name():
    # docTab disables wiki panel → WikiName not submitted
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main")
    assert ConfigKeys.WIKI_NAME not in cfg


def test_doc_repo_does_not_store_path_filter():
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main")
    assert ConfigKeys.PATH_FILTER not in cfg


def test_doc_repo_wiki_fields_not_stored_even_if_submitted():
    # Even if browser submits wiki-panel fields, parser ignores them for DocType==repo
    cfg = _doc_repo(ConfigRepository="Docs", ConfigBranch="main",
                    ConfigWikiName="ShouldBeIgnored",
                    ConfigPathFilter="ShouldBeIgnored")
    assert ConfigKeys.WIKI_NAME not in cfg
    assert ConfigKeys.PATH_FILTER not in cfg


# ── ADO connection fields (using wiki subcard as representative) ───────────────

def test_doc_ado_base_url_stored():
    cfg = _doc_wiki(ConfigBaseUrl="https://dev.azure.com/myorg",
                    ConfigWikiName="MyWiki")
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg"


def test_doc_ado_api_version_stored():
    cfg = _doc_wiki(ConfigApiVersion="7.1", ConfigWikiName="MyWiki")
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_doc_verify_ssl_stored():
    cfg = _doc_wiki(ConfigVerifySSL="false", ConfigWikiName="MyWiki")
    assert cfg[ConfigKeys.VERIFY_SSL] == "false"


# ── Auth type subcards (ADO connection) ───────────────────────────────────────

def test_doc_auth_pat_stores_pat_only():
    cfg = _doc_wiki(ConfigWikiName="MyWiki",
                    ConfigAuthType="pat",
                    ConfigPat="MY_PAT",
                    ConfigToken="SHOULD_BE_IGNORED")
    assert cfg[ConfigKeys.PAT] == "MY_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_doc_auth_bearer_stores_token_only():
    cfg = _doc_wiki(ConfigWikiName="MyWiki",
                    ConfigAuthType="bearer",
                    ConfigPat="SHOULD_BE_IGNORED",
                    ConfigToken="MY_BEARER_TOKEN")
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_doc_auth_ntlm_stores_windows_credentials():
    cfg = _doc_wiki(ConfigWikiName="MyWiki",
                    ConfigAuthType="ntlm",
                    ConfigUsername="CORP\\svc",
                    ConfigPassword="MY_PASSWORD",
                    ConfigDomain="CORP")
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_doc_auth_negotiate_stores_windows_credentials():
    cfg = _doc_wiki(ConfigWikiName="MyWiki",
                    ConfigAuthType="negotiate",
                    ConfigUsername="user",
                    ConfigPassword="pass",
                    ConfigDomain="DOM")
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_doc_auth_apikey_stores_header_and_value():
    cfg = _doc_wiki(ConfigWikiName="MyWiki",
                    ConfigAuthType="apikey",
                    ConfigApiKeyHeader="X-Api-Key",
                    ConfigApiKeyValue="MY_SECRET")
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET"


def test_doc_auth_none_stores_no_credentials():
    cfg = _doc_wiki(ConfigWikiName="MyWiki",
                    ConfigAuthType="none",
                    ConfigPat="ignored", ConfigToken="ignored",
                    ConfigUsername="ignored", ConfigPassword="ignored",
                    ConfigApiKeyValue="ignored")
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_doc_auth_ntlm_does_not_store_pat():
    cfg = _doc_wiki(ConfigWikiName="MyWiki",
                    ConfigAuthType="ntlm",
                    ConfigPat="SHOULD_BE_IGNORED",
                    ConfigUsername="u", ConfigPassword="p", ConfigDomain="d")
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with documentation source type ───────────────────
# sysTab('custom') disables ado-provider-content: ConfigDocType is disabled →
# server defaults DocType="wiki". Wiki branch does NOT set GlobPatterns,
# so no GlobPatterns server-default leakage (unlike Code Repo).

def test_doc_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/docs",
               ConfigHttpMethod="GET")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/docs"
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_doc_custom_provider_stores_response_mapping():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/docs",
               ConfigItemsPath="value",
               ConfigTitleField="title",
               ConfigContentFields="body,summary")
    assert cfg[ConfigKeys.ITEMS_PATH] == "value"
    assert cfg[ConfigKeys.TITLE_FIELD] == "title"
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "body,summary"


def test_doc_custom_provider_doc_type_wiki_server_default_leaks():
    # ConfigDocType is disabled by sysTab → absent from form → server defaults "wiki"
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/docs")
    assert cfg.get(ConfigKeys.DOC_TYPE) == "wiki"


def test_doc_custom_provider_no_glob_patterns_leakage():
    # Wiki branch never sets GlobPatterns → no leakage (unlike Code Repo)
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/docs")
    assert ConfigKeys.GLOB_PATTERNS not in cfg


def test_doc_custom_provider_no_wiki_name_stored():
    # sysTab disables ado-provider-content → WikiName not submitted
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/docs")
    assert ConfigKeys.WIKI_NAME not in cfg


def test_doc_custom_provider_no_ado_connection_fields():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/docs")
    assert ConfigKeys.BASE_URL not in cfg
    assert ConfigKeys.API_VERSION not in cfg


# ── Provider tab: Manual with documentation source type ──────────────────────

def test_doc_manual_provider_text_stores_content_and_title():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Architecture Docs",
               ConfigManualText="Markdown documentation here")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "Markdown documentation here"
    assert cfg[ConfigKeys.TITLE] == "Architecture Docs"


def test_doc_manual_provider_upload_carries_existing_content():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualExistingContent="Previously uploaded docs")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded docs"


def test_doc_manual_provider_doc_type_wiki_leaks():
    # Same server-default leakage as custom provider
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="docs")
    assert cfg.get(ConfigKeys.DOC_TYPE) == "wiki"


def test_doc_manual_provider_no_glob_patterns_leakage():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="docs")
    assert ConfigKeys.GLOB_PATTERNS not in cfg


def test_doc_manual_provider_no_wiki_name_stored():
    # sysTab disables ado-provider-content → WikiName not submitted
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="docs")
    assert ConfigKeys.WIKI_NAME not in cfg


def test_doc_manual_provider_no_ado_connection_fields():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="docs")
    assert ConfigKeys.BASE_URL not in cfg
    assert ConfigKeys.API_VERSION not in cfg
