"""
Tests for _build_source_from_form – Test Case source type.

UI layout: three subcards inside the ADO provider card — identical structure
to Requirements but with one key difference: the Filters panel has only ONE
item type checkbox ("Test Case"), always pre-checked.

  [Filters tab]       ConfigItemTypes ("Test Case" only), ConfigAreaPath, ConfigIterationPath
  [Custom WIQL tab]   ConfigQuery, ConfigFields
  [Repo Files tab]    ConfigRepository, ConfigBranch, ConfigGlobPatterns

ConfigTcType is a real hidden field always submitted; tcTab() updates its
value and disables inactive panels' inputs.

Create default item type: "Test Case" (checked)
Edit default item type:   cfg.get('ItemTypes', 'Test Case')
GlobPatterns default:     "**/*.md" (server and template)
Server-default leakage for non-ADO providers: TcType="filters" leaks in.
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helpers ───────────────────────────────────────────────────────────────────

def _tc_filters(**fields) -> dict:
    """Simulates Filters tab active: ConfigTcType=filters, custom/repo inputs disabled."""
    return _cfg(SourceTypes.TEST_CASE, ConfigTcType="filters", **fields)


def _tc_custom(**fields) -> dict:
    """Simulates Custom WIQL tab active: ConfigTcType=custom, filter/repo inputs disabled."""
    return _cfg(SourceTypes.TEST_CASE, ConfigTcType="custom", **fields)


def _tc_repo(**fields) -> dict:
    """Simulates Repo Files tab active: ConfigTcType=repo, filter/custom inputs disabled."""
    return _cfg(SourceTypes.TEST_CASE, ConfigTcType="repo", **fields)


# ── ConfigTcType hidden field ─────────────────────────────────────────────────

def test_tc_type_defaults_to_filters_when_missing():
    cfg = _cfg(SourceTypes.TEST_CASE)
    assert cfg[ConfigKeys.TC_TYPE] == "filters"


def test_tc_type_filters_stored():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.TC_TYPE] == "filters"


def test_tc_type_custom_stored():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert cfg[ConfigKeys.TC_TYPE] == "custom"


def test_tc_type_repo_stored():
    cfg = _tc_repo(ConfigRepository="TestRepo", ConfigBranch="main")
    assert cfg[ConfigKeys.TC_TYPE] == "repo"


# ── Filters subcard: item type ────────────────────────────────────────────────

def test_tc_filters_test_case_item_type_stored():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Test Case"


def test_tc_filters_item_type_absent_when_not_submitted():
    cfg = _tc_filters()
    assert ConfigKeys.ITEM_TYPES not in cfg


def test_tc_filters_default_create_item_type():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Test Case"


# ── Filters subcard: path filters ─────────────────────────────────────────────

def test_tc_filters_area_path_stored():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]},
                      ConfigAreaPath="MyProject\\QA Team")
    assert cfg[ConfigKeys.AREA_PATH] == "MyProject\\QA Team"


def test_tc_filters_iteration_path_stored():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]},
                      ConfigIterationPath="MyProject\\Sprint 4")
    assert cfg[ConfigKeys.ITERATION_PATH] == "MyProject\\Sprint 4"


def test_tc_filters_both_paths_stored():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]},
                      ConfigAreaPath="Proj\\Area",
                      ConfigIterationPath="Proj\\Sprint 2")
    assert cfg[ConfigKeys.AREA_PATH] == "Proj\\Area"
    assert cfg[ConfigKeys.ITERATION_PATH] == "Proj\\Sprint 2"


def test_tc_filters_no_paths_not_in_config():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.ITERATION_PATH not in cfg


# ── Filters subcard: other panels' inputs absent (browser disabled them) ──────

def test_tc_filters_query_absent():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert ConfigKeys.QUERY not in cfg


def test_tc_filters_fields_absent():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert ConfigKeys.FIELDS not in cfg


def test_tc_filters_repository_absent():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert ConfigKeys.REPOSITORY not in cfg


def test_tc_filters_branch_absent():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert ConfigKeys.BRANCH not in cfg


def test_tc_filters_glob_patterns_absent():
    cfg = _tc_filters(**{"ConfigItemTypes": ["Test Case"]})
    assert ConfigKeys.GLOB_PATTERNS not in cfg


# ── Custom WIQL subcard ────────────────────────────────────────────────────────

def test_tc_custom_query_stored():
    cfg = _tc_custom(ConfigQuery="SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = 'Test Case'")
    assert cfg[ConfigKeys.QUERY] == "SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = 'Test Case'"


def test_tc_custom_fields_stored():
    cfg = _tc_custom(ConfigQuery="SELECT ...",
                     ConfigFields="System.Title,System.Description,Microsoft.VSTS.TCM.Steps")
    assert cfg[ConfigKeys.FIELDS] == "System.Title,System.Description,Microsoft.VSTS.TCM.Steps"


def test_tc_custom_fields_absent_when_blank():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.FIELDS not in cfg


def test_tc_custom_item_types_absent():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.ITEM_TYPES not in cfg


def test_tc_custom_area_path_absent():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.AREA_PATH not in cfg


def test_tc_custom_iteration_path_absent():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.ITERATION_PATH not in cfg


def test_tc_custom_repository_absent():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.REPOSITORY not in cfg


def test_tc_custom_branch_absent():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.BRANCH not in cfg


def test_tc_custom_glob_patterns_absent():
    cfg = _tc_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.GLOB_PATTERNS not in cfg


# ── Repo Files subcard ────────────────────────────────────────────────────────

def test_tc_repo_repository_stored():
    cfg = _tc_repo(ConfigRepository="TestSpecsRepo", ConfigBranch="main")
    assert cfg[ConfigKeys.REPOSITORY] == "TestSpecsRepo"


def test_tc_repo_branch_stored():
    cfg = _tc_repo(ConfigRepository="TestSpecsRepo", ConfigBranch="feature/bdd")
    assert cfg[ConfigKeys.BRANCH] == "feature/bdd"


def test_tc_repo_branch_optional_absent_when_not_submitted():
    cfg = _tc_repo(ConfigRepository="TestSpecsRepo")
    assert ConfigKeys.BRANCH not in cfg


def test_tc_repo_glob_patterns_stored():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main",
                   ConfigGlobPatterns="**/*.feature")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.feature"


def test_tc_repo_glob_bdd_spec_pattern():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main",
                   ConfigGlobPatterns="**/*.feature,tests/**/*.spec")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.feature,tests/**/*.spec"


def test_tc_repo_glob_template_default_md():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main",
                   ConfigGlobPatterns="**/*.md")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.md"


def test_tc_repo_glob_server_default_when_absent():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.md"


def test_tc_repo_item_types_absent():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main")
    assert ConfigKeys.ITEM_TYPES not in cfg


def test_tc_repo_area_path_absent():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main")
    assert ConfigKeys.AREA_PATH not in cfg


def test_tc_repo_iteration_path_absent():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main")
    assert ConfigKeys.ITERATION_PATH not in cfg


def test_tc_repo_query_absent():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main")
    assert ConfigKeys.QUERY not in cfg


def test_tc_repo_fields_absent():
    cfg = _tc_repo(ConfigRepository="R", ConfigBranch="main")
    assert ConfigKeys.FIELDS not in cfg


# ── ADO connection card ────────────────────────────────────────────────────────

def test_tc_ado_base_url_stored():
    cfg = _tc_filters(ConfigBaseUrl="https://dev.azure.com/myorg/myproject",
                      **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_tc_ado_api_version_stored():
    cfg = _tc_filters(ConfigApiVersion="7.1", **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_tc_verify_ssl_stored():
    cfg = _tc_filters(ConfigVerifySSL="false", **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.VERIFY_SSL] == "false"


# ── Auth type subcards (ADO connection) ───────────────────────────────────────

def test_tc_auth_pat_stores_pat_only():
    cfg = _tc_filters(ConfigAuthType="pat",
                      ConfigPat="TFS_PAT",
                      ConfigToken="SHOULD_BE_IGNORED",
                      **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.PAT] == "TFS_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_tc_auth_bearer_stores_token_only():
    cfg = _tc_filters(ConfigAuthType="bearer",
                      ConfigPat="SHOULD_BE_IGNORED",
                      ConfigToken="MY_BEARER_TOKEN",
                      **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_tc_auth_ntlm_stores_windows_credentials():
    cfg = _tc_filters(ConfigAuthType="ntlm",
                      ConfigUsername="CORP\\svc-account",
                      ConfigPassword="MY_PASSWORD",
                      ConfigDomain="CORP",
                      **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc-account"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_tc_auth_negotiate_stores_windows_credentials():
    cfg = _tc_filters(ConfigAuthType="negotiate",
                      ConfigUsername="user",
                      ConfigPassword="pass",
                      ConfigDomain="DOM",
                      **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_tc_auth_apikey_stores_header_and_value():
    cfg = _tc_filters(ConfigAuthType="apikey",
                      ConfigApiKeyHeader="X-Api-Key",
                      ConfigApiKeyValue="MY_SECRET_KEY",
                      **{"ConfigItemTypes": ["Test Case"]})
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET_KEY"


def test_tc_auth_none_stores_no_credentials():
    cfg = _tc_filters(ConfigAuthType="none",
                      ConfigPat="ignored", ConfigToken="ignored",
                      ConfigUsername="ignored", ConfigPassword="ignored",
                      ConfigApiKeyValue="ignored",
                      **{"ConfigItemTypes": ["Test Case"]})
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_tc_auth_ntlm_does_not_store_pat():
    cfg = _tc_filters(ConfigAuthType="ntlm",
                      ConfigPat="SHOULD_BE_IGNORED",
                      ConfigUsername="u", ConfigPassword="p", ConfigDomain="d",
                      **{"ConfigItemTypes": ["Test Case"]})
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with test-case source type ───────────────────────

def test_tc_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testcases",
               ConfigHttpMethod="GET")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/testcases"
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_tc_custom_provider_stores_response_mapping():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testcases",
               ConfigItemsPath="value",
               ConfigTitleField="name",
               ConfigContentFields="steps,expectedResult")
    assert cfg[ConfigKeys.ITEMS_PATH] == "value"
    assert cfg[ConfigKeys.TITLE_FIELD] == "name"
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "steps,expectedResult"


def test_tc_custom_provider_ado_type_fields_absent():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testcases")
    assert ConfigKeys.ITEM_TYPES not in cfg
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.QUERY not in cfg
    assert ConfigKeys.REPOSITORY not in cfg


def test_tc_custom_provider_tc_type_server_default_leaks():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testcases")
    assert cfg.get(ConfigKeys.TC_TYPE) == "filters"


# ── Provider tab: Manual with test-case source type ───────────────────────────

def test_tc_manual_provider_text_stores_content_and_title():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Test Case Export",
               ConfigManualText="Test case content pasted here")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "Test case content pasted here"
    assert cfg[ConfigKeys.TITLE] == "Test Case Export"


def test_tc_manual_provider_upload_carries_existing_content():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualExistingContent="Previously uploaded test cases")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded test cases"


def test_tc_manual_provider_ado_type_fields_absent():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="content")
    assert ConfigKeys.ITEM_TYPES not in cfg
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.QUERY not in cfg
    assert ConfigKeys.REPOSITORY not in cfg


def test_tc_manual_provider_tc_type_server_default_leaks():
    cfg = _cfg(SourceTypes.TEST_CASE,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="content")
    assert cfg.get(ConfigKeys.TC_TYPE) == "filters"
