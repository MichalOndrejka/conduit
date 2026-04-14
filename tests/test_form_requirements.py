"""
Tests for _build_source_from_form – Requirements source type.

UI layout: three subcards inside the ADO provider card.

  [Filters tab]       ConfigItemTypes (checkboxes), ConfigAreaPath, ConfigIterationPath
  [Custom WIQL tab]   ConfigQuery, ConfigFields
  [Repo Files tab]    ConfigRepository, ConfigBranch, ConfigGlobPatterns

Unlike Work Items, the active subcard IS sent as a hidden field: ConfigReqType.
reqTab() updates that hidden value AND disables the inactive panels' inputs,
so only the active panel's fields reach the server alongside ConfigReqType.

Default item types on create/edit (all 3 checked): "Product Requirement",
"Software Requirement", "Risk".
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helpers ───────────────────────────────────────────────────────────────────

def _req_filters(**fields) -> dict:
    """Simulates Filters tab active: ConfigReqType=filters, filter inputs enabled."""
    return _cfg(SourceTypes.REQUIREMENTS, ConfigReqType="filters", **fields)


def _req_custom(**fields) -> dict:
    """Simulates Custom WIQL tab active: ConfigReqType=custom, filter/repo inputs disabled."""
    return _cfg(SourceTypes.REQUIREMENTS, ConfigReqType="custom", **fields)


def _req_repo(**fields) -> dict:
    """Simulates Repo Files tab active: ConfigReqType=repo, filter/custom inputs disabled."""
    return _cfg(SourceTypes.REQUIREMENTS, ConfigReqType="repo", **fields)


# ── ConfigReqType hidden field ────────────────────────────────────────────────

def test_req_type_defaults_to_filters_when_missing():
    cfg = _cfg(SourceTypes.REQUIREMENTS)
    assert cfg[ConfigKeys.REQ_TYPE] == "filters"


def test_req_type_filters_stored():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.REQ_TYPE] == "filters"


def test_req_type_custom_stored():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert cfg[ConfigKeys.REQ_TYPE] == "custom"


def test_req_type_repo_stored():
    cfg = _req_repo(ConfigRepository="Docs", ConfigBranch="main")
    assert cfg[ConfigKeys.REQ_TYPE] == "repo"


# ── Filters subcard: item types ───────────────────────────────────────────────

def test_req_filters_all_three_default_types():
    cfg = _req_filters(**{"ConfigItemTypes": ["Product Requirement", "Software Requirement", "Risk"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Product Requirement,Software Requirement,Risk"


def test_req_filters_product_requirement_only():
    cfg = _req_filters(**{"ConfigItemTypes": ["Product Requirement"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Product Requirement"


def test_req_filters_software_requirement_only():
    cfg = _req_filters(**{"ConfigItemTypes": ["Software Requirement"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Software Requirement"


def test_req_filters_risk_only():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Risk"


def test_req_filters_two_types():
    cfg = _req_filters(**{"ConfigItemTypes": ["Software Requirement", "Risk"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Software Requirement,Risk"


def test_req_filters_no_types_not_in_config():
    cfg = _req_filters()
    assert ConfigKeys.ITEM_TYPES not in cfg


# ── Filters subcard: path filters ─────────────────────────────────────────────

def test_req_filters_area_path_stored():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]}, ConfigAreaPath="MyProject\\Team A")
    assert cfg[ConfigKeys.AREA_PATH] == "MyProject\\Team A"


def test_req_filters_iteration_path_stored():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]}, ConfigIterationPath="MyProject\\Sprint 3")
    assert cfg[ConfigKeys.ITERATION_PATH] == "MyProject\\Sprint 3"


def test_req_filters_both_paths_stored():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]},
                       ConfigAreaPath="Proj\\Area",
                       ConfigIterationPath="Proj\\Sprint 5")
    assert cfg[ConfigKeys.AREA_PATH] == "Proj\\Area"
    assert cfg[ConfigKeys.ITERATION_PATH] == "Proj\\Sprint 5"


def test_req_filters_no_paths_not_in_config():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.ITERATION_PATH not in cfg


# ── Filters subcard: other panels' inputs are absent (browser disabled them) ──

def test_req_filters_query_absent():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert ConfigKeys.QUERY not in cfg


def test_req_filters_fields_absent():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert ConfigKeys.FIELDS not in cfg


def test_req_filters_repository_absent():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert ConfigKeys.REPOSITORY not in cfg


def test_req_filters_branch_absent():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert ConfigKeys.BRANCH not in cfg


def test_req_filters_glob_patterns_absent():
    cfg = _req_filters(**{"ConfigItemTypes": ["Risk"]})
    assert ConfigKeys.GLOB_PATTERNS not in cfg


# ── Custom WIQL subcard ────────────────────────────────────────────────────────

def test_req_custom_query_stored():
    cfg = _req_custom(ConfigQuery="SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = 'Risk'")
    assert cfg[ConfigKeys.QUERY] == "SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = 'Risk'"


def test_req_custom_fields_stored():
    cfg = _req_custom(ConfigQuery="SELECT ...", ConfigFields="System.Title,System.Description")
    assert cfg[ConfigKeys.FIELDS] == "System.Title,System.Description"


def test_req_custom_fields_absent_when_blank():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.FIELDS not in cfg


def test_req_custom_item_types_absent():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.ITEM_TYPES not in cfg


def test_req_custom_area_path_absent():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.AREA_PATH not in cfg


def test_req_custom_iteration_path_absent():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.ITERATION_PATH not in cfg


def test_req_custom_repository_absent():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.REPOSITORY not in cfg


def test_req_custom_branch_absent():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.BRANCH not in cfg


def test_req_custom_glob_patterns_absent():
    cfg = _req_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.GLOB_PATTERNS not in cfg


# ── Repo Files subcard ────────────────────────────────────────────────────────

def test_req_repo_repository_stored():
    cfg = _req_repo(ConfigRepository="RequirementsRepo", ConfigBranch="main")
    assert cfg[ConfigKeys.REPOSITORY] == "RequirementsRepo"


def test_req_repo_branch_stored():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="develop")
    assert cfg[ConfigKeys.BRANCH] == "develop"


def test_req_repo_branch_optional_absent_when_not_submitted():
    cfg = _req_repo(ConfigRepository="Repo")
    assert ConfigKeys.BRANCH not in cfg


def test_req_repo_glob_patterns_stored():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main",
                    ConfigGlobPatterns="docs/**/*.md,specs/**/*.txt")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "docs/**/*.md,specs/**/*.txt"


def test_req_repo_glob_patterns_default():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main",
                    ConfigGlobPatterns="**/*.md")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.md"


def test_req_repo_glob_patterns_server_default_when_absent():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main")
    assert cfg[ConfigKeys.GLOB_PATTERNS] == "**/*.md"


def test_req_repo_item_types_absent():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main")
    assert ConfigKeys.ITEM_TYPES not in cfg


def test_req_repo_area_path_absent():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main")
    assert ConfigKeys.AREA_PATH not in cfg


def test_req_repo_iteration_path_absent():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main")
    assert ConfigKeys.ITERATION_PATH not in cfg


def test_req_repo_query_absent():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main")
    assert ConfigKeys.QUERY not in cfg


def test_req_repo_fields_absent():
    cfg = _req_repo(ConfigRepository="Repo", ConfigBranch="main")
    assert ConfigKeys.FIELDS not in cfg


# ── ADO connection card ────────────────────────────────────────────────────────

def test_req_ado_base_url_stored():
    cfg = _req_filters(ConfigBaseUrl="https://dev.azure.com/myorg/myproject",
                       **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_req_ado_api_version_stored():
    cfg = _req_filters(ConfigApiVersion="7.1", **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_req_verify_ssl_stored():
    cfg = _req_filters(ConfigVerifySSL="false", **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.VERIFY_SSL] == "false"


# ── Auth type subcards (ADO connection) ───────────────────────────────────────

def test_req_auth_pat_stores_pat_only():
    cfg = _req_filters(ConfigAuthType="pat",
                       ConfigPat="TFS_PAT",
                       ConfigToken="SHOULD_BE_IGNORED",
                       **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.PAT] == "TFS_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_req_auth_bearer_stores_token_only():
    cfg = _req_filters(ConfigAuthType="bearer",
                       ConfigPat="SHOULD_BE_IGNORED",
                       ConfigToken="MY_BEARER_TOKEN",
                       **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_req_auth_ntlm_stores_windows_credentials():
    cfg = _req_filters(ConfigAuthType="ntlm",
                       ConfigUsername="CORP\\svc-account",
                       ConfigPassword="MY_PASSWORD",
                       ConfigDomain="CORP",
                       **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc-account"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_req_auth_negotiate_stores_windows_credentials():
    cfg = _req_filters(ConfigAuthType="negotiate",
                       ConfigUsername="user",
                       ConfigPassword="pass",
                       ConfigDomain="DOM",
                       **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_req_auth_apikey_stores_header_and_value():
    cfg = _req_filters(ConfigAuthType="apikey",
                       ConfigApiKeyHeader="X-Api-Key",
                       ConfigApiKeyValue="MY_SECRET_KEY",
                       **{"ConfigItemTypes": ["Risk"]})
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET_KEY"


def test_req_auth_none_stores_no_credentials():
    cfg = _req_filters(ConfigAuthType="none",
                       ConfigPat="ignored", ConfigToken="ignored",
                       ConfigUsername="ignored", ConfigPassword="ignored",
                       ConfigApiKeyValue="ignored",
                       **{"ConfigItemTypes": ["Risk"]})
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_req_auth_ntlm_does_not_store_pat():
    cfg = _req_filters(ConfigAuthType="ntlm",
                       ConfigPat="SHOULD_BE_IGNORED",
                       ConfigUsername="u", ConfigPassword="p", ConfigDomain="d",
                       **{"ConfigItemTypes": ["Risk"]})
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with requirements source type ────────────────────

def test_req_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.REQUIREMENTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/requirements",
               ConfigHttpMethod="POST")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/requirements"
    assert cfg[ConfigKeys.HTTP_METHOD] == "POST"


def test_req_custom_provider_stores_response_mapping():
    cfg = _cfg(SourceTypes.REQUIREMENTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/requirements",
               ConfigItemsPath="data.items",
               ConfigTitleField="name",
               ConfigContentFields="description,acceptance_criteria")
    assert cfg[ConfigKeys.ITEMS_PATH] == "data.items"
    assert cfg[ConfigKeys.TITLE_FIELD] == "name"
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "description,acceptance_criteria"


def test_req_custom_provider_does_not_store_ado_type_fields():
    cfg = _cfg(SourceTypes.REQUIREMENTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/requirements")
    assert ConfigKeys.ITEM_TYPES not in cfg
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.QUERY not in cfg
    assert ConfigKeys.REPOSITORY not in cfg


# ── Provider tab: Manual with requirements source type ────────────────────────

def test_req_manual_provider_text_stores_content_and_title():
    cfg = _cfg(SourceTypes.REQUIREMENTS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Requirements Export",
               ConfigManualText="Detailed requirements content here")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "Detailed requirements content here"
    assert cfg[ConfigKeys.TITLE] == "Requirements Export"


def test_req_manual_provider_upload_carries_existing_content():
    cfg = _cfg(SourceTypes.REQUIREMENTS,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualTitle="Spec",
               ConfigManualExistingContent="Previously uploaded requirements")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded requirements"


def test_req_manual_provider_does_not_store_ado_type_fields():
    cfg = _cfg(SourceTypes.REQUIREMENTS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="content")
    assert ConfigKeys.ITEM_TYPES not in cfg
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.QUERY not in cfg
    assert ConfigKeys.REPOSITORY not in cfg
