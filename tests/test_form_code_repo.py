"""
Tests for _build_source_from_form – Code Repo source type.

UI layout: single static card — no subcard switching whatsoever.
The "Filters" tab label is purely decorative (no JS, no hidden mode key).
All three fields are always enabled and always submitted.

  ConfigRepository   (required)
  ConfigBranch       (pre-filled "Main" on create; cfg.get('Branch','Main') on edit)
  ConfigGlobPatterns (pre-filled "**/*.cs"; server default also "**/*.cs")

Server-default leakage: GlobPatterns="**/*.cs" leaks for non-ADO providers
since the CODE_REPO type block runs unconditionally.
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helper ────────────────────────────────────────────────────────────────────

def _code(**fields) -> dict:
    return _cfg(SourceTypes.CODE_REPO, **fields)


# ── Core fields ───────────────────────────────────────────────────────────────

def test_code_repository_stored():
    cfg = _code(ConfigRepository="MyRepo", ConfigBranch="Main")
    assert cfg[ConfigKeys.REPOSITORY] == "MyRepo"


def test_code_branch_stored():
    cfg = _code(ConfigRepository="MyRepo", ConfigBranch="develop")
    assert cfg[ConfigKeys.BRANCH] == "develop"


def test_code_branch_default_main_from_create_template():
    cfg = _code(ConfigRepository="MyRepo", ConfigBranch="Main")
    assert cfg[ConfigKeys.BRANCH] == "Main"


def test_code_branch_absent_when_field_empty():
    cfg = _code(ConfigRepository="MyRepo", ConfigBranch="")
    assert ConfigKeys.BRANCH not in cfg


def test_code_branch_absent_when_not_submitted():
    cfg = _code(ConfigRepository="MyRepo")
    assert ConfigKeys.BRANCH not in cfg


def test_code_repository_and_branch_together():
    cfg = _code(ConfigRepository="BackendService", ConfigBranch="release/2.0")
    assert cfg[ConfigKeys.REPOSITORY] == "BackendService"
    assert cfg[ConfigKeys.BRANCH] == "release/2.0"


# ── Glob patterns ─────────────────────────────────────────────────────────────

def test_code_glob_server_default_when_absent():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.cs"


def test_code_glob_template_prefill_cs():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns="**/*.cs")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.cs"


def test_code_glob_single_typescript():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns="**/*.ts")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.ts"


def test_code_glob_multiple_patterns():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigGlobPatterns="**/*.cs,**/*.ts")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.cs,**/*.ts"


def test_code_glob_react_patterns():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigGlobPatterns="**/*.ts,**/*.tsx,**/*.js,**/*.jsx")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.ts,**/*.tsx,**/*.js,**/*.jsx"


def test_code_glob_python_patterns():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns="**/*.py")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.py"


def test_code_glob_java_patterns():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns="**/*.java")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.java"


def test_code_glob_scoped_to_src_directory():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigGlobPatterns="src/**/*.cs,tests/**/*.cs")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "src/**/*.cs,tests/**/*.cs"


def test_code_glob_excludes_nothing_extra():
    pattern = "**/*.cs,!**/obj/**,!**/bin/**"
    cfg = _code(ConfigRepository="R", ConfigBranch="Main", ConfigGlobPatterns=pattern)
    assert cfg[ConfigKeys.GLOB_PATTERNS] == pattern


# ── ADO connection card ────────────────────────────────────────────────────────

def test_code_ado_base_url_stored():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigBaseUrl="https://dev.azure.com/myorg/myproject")
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_code_ado_api_version_stored():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main", ConfigApiVersion="7.1")
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_code_verify_ssl_not_stored():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main", ConfigVerifySSL="false")
    assert ConfigKeys.VERIFY_SSL not in cfg  # BUG: VerifySSL is silently dropped


# ── Auth type subcards (ADO connection) ───────────────────────────────────────

def test_code_auth_pat_stores_pat_only():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigAuthType="pat",
                ConfigPat="TFS_PAT",
                ConfigToken="SHOULD_BE_IGNORED")
    assert cfg[ConfigKeys.PAT] == "TFS_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_code_auth_bearer_stores_token_only():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigAuthType="bearer",
                ConfigPat="SHOULD_BE_IGNORED",
                ConfigToken="MY_BEARER_TOKEN")
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_code_auth_ntlm_stores_windows_credentials():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigAuthType="ntlm",
                ConfigUsername="CORP\\svc-account",
                ConfigPassword="MY_PASSWORD",
                ConfigDomain="CORP")
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc-account"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_code_auth_negotiate_stores_windows_credentials():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigAuthType="negotiate",
                ConfigUsername="user",
                ConfigPassword="pass",
                ConfigDomain="DOM")
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_code_auth_apikey_stores_header_and_value():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigAuthType="apikey",
                ConfigApiKeyHeader="X-Api-Key",
                ConfigApiKeyValue="MY_SECRET_KEY")
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET_KEY"


def test_code_auth_none_stores_no_credentials():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigAuthType="none",
                ConfigPat="ignored", ConfigToken="ignored",
                ConfigUsername="ignored", ConfigPassword="ignored",
                ConfigApiKeyValue="ignored")
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_code_auth_ntlm_does_not_store_pat():
    cfg = _code(ConfigRepository="R", ConfigBranch="Main",
                ConfigAuthType="ntlm",
                ConfigPat="SHOULD_BE_IGNORED",
                ConfigUsername="u", ConfigPassword="p", ConfigDomain="d")
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with code source type ────────────────────────────

def test_code_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.CODE_REPO,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/code",
               ConfigHttpMethod="GET")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/code"
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_code_custom_provider_stores_response_mapping():
    cfg = _cfg(SourceTypes.CODE_REPO,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/code",
               ConfigItemsPath="files",
               ConfigTitleField="path",
               ConfigContentFields="content")
    assert cfg[ConfigKeys.ITEMS_PATH] == "files"
    assert cfg[ConfigKeys.TITLE_FIELD] == "path"
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "content"


def test_code_custom_provider_does_not_store_repository_or_branch():
    cfg = _cfg(SourceTypes.CODE_REPO,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/code")
    assert ConfigKeys.REPOSITORY not in cfg
    assert ConfigKeys.BRANCH not in cfg


def test_code_custom_provider_glob_patterns_server_default_leaks():
    # _set(GLOB_PATTERNS, ..., default="**/*.cs") fires unconditionally for CODE_REPO
    cfg = _cfg(SourceTypes.CODE_REPO,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/code")
    assert cfg.get(ConfigKeys.GLOB_PATTERNS) == "**/*.cs"


# ── Provider tab: Manual with code source type ────────────────────────────────

def test_code_manual_provider_text_stores_content_and_title():
    cfg = _cfg(SourceTypes.CODE_REPO,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Code Snippets",
               ConfigManualText="def hello(): pass")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "def hello(): pass"
    assert cfg[ConfigKeys.TITLE] == "Code Snippets"


def test_code_manual_provider_upload_carries_existing_content():
    cfg = _cfg(SourceTypes.CODE_REPO,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualExistingContent="Previously uploaded code")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded code"


def test_code_manual_provider_does_not_store_repository_or_branch():
    cfg = _cfg(SourceTypes.CODE_REPO,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="content")
    assert ConfigKeys.REPOSITORY not in cfg
    assert ConfigKeys.BRANCH not in cfg
