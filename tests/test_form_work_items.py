"""
Tests for _build_source_from_form – Work Items source type.

UI layout: two subcards inside the ADO provider card.

  [Filters tab]       ConfigItemTypes (checkboxes), ConfigAreaPath, ConfigIterationPath
  [Custom WIQL tab]   ConfigQuery, ConfigFields

The JS wiTab() helper *disables* inputs in the inactive panel, so the browser
never submits them.  Tests that model a specific tab being active must omit the
other panel's fields, exactly as a real browser would.

The active tab is NOT sent as a hidden value.  On reload the template detects
the active tab by checking whether cfg.get('Query') is non-empty.
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helpers ───────────────────────────────────────────────────────────────────

def _wi_filters(**fields) -> dict:
    """Form payload when the Filters tab is active (Custom WIQL inputs disabled/absent)."""
    return _cfg(SourceTypes.WORK_ITEM_QUERY, **fields)


def _wi_custom(**fields) -> dict:
    """Form payload when the Custom WIQL tab is active (filter inputs disabled/absent)."""
    return _cfg(SourceTypes.WORK_ITEM_QUERY, **fields)


# ── Filters subcard: item types ───────────────────────────────────────────────

def test_wi_filters_all_six_item_types():
    cfg = _wi_filters(**{"ConfigItemTypes": ["Epic", "Feature", "User Story", "Bug", "Defect", "Task"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Epic,Feature,User Story,Bug,Defect,Task"


def test_wi_filters_default_create_types_no_task():
    # On create the template pre-checks Epic/Feature/User Story/Bug/Defect (not Task)
    cfg = _wi_filters(**{"ConfigItemTypes": ["Epic", "Feature", "User Story", "Bug", "Defect"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Epic,Feature,User Story,Bug,Defect"
    assert "Task" not in cfg[ConfigKeys.ITEM_TYPES]


def test_wi_filters_single_type_task():
    cfg = _wi_filters(**{"ConfigItemTypes": ["Task"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Task"


def test_wi_filters_two_types():
    cfg = _wi_filters(**{"ConfigItemTypes": ["Bug", "Task"]})
    assert cfg[ConfigKeys.ITEM_TYPES] == "Bug,Task"


def test_wi_filters_no_types_not_in_config():
    # Edge case: no checkbox was checked (JS normally prevents this)
    cfg = _wi_filters()
    assert ConfigKeys.ITEM_TYPES not in cfg


# ── Filters subcard: path filters ─────────────────────────────────────────────

def test_wi_filters_area_path_stored():
    cfg = _wi_filters(**{"ConfigItemTypes": ["Bug"]}, ConfigAreaPath="MyProject\\Team A")
    assert cfg[ConfigKeys.AREA_PATH] == "MyProject\\Team A"


def test_wi_filters_iteration_path_stored():
    cfg = _wi_filters(**{"ConfigItemTypes": ["Bug"]}, ConfigIterationPath="MyProject\\Sprint 1")
    assert cfg[ConfigKeys.ITERATION_PATH] == "MyProject\\Sprint 1"


def test_wi_filters_both_paths_stored():
    cfg = _wi_filters(**{"ConfigItemTypes": ["Bug"]},
                      ConfigAreaPath="Proj\\Area",
                      ConfigIterationPath="Proj\\Sprint 5")
    assert cfg[ConfigKeys.AREA_PATH] == "Proj\\Area"
    assert cfg[ConfigKeys.ITERATION_PATH] == "Proj\\Sprint 5"


def test_wi_filters_no_paths_not_in_config():
    cfg = _wi_filters(**{"ConfigItemTypes": ["Bug"]})
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.ITERATION_PATH not in cfg


# ── Filters subcard: custom WIQL fields absent (browser disabled them) ─────────

def test_wi_filters_query_absent():
    # Filters tab active → browser does not submit ConfigQuery
    cfg = _wi_filters(**{"ConfigItemTypes": ["Bug"]})
    assert ConfigKeys.QUERY not in cfg


def test_wi_filters_fields_absent():
    # Filters tab active → browser does not submit ConfigFields
    cfg = _wi_filters(**{"ConfigItemTypes": ["Bug"]})
    assert ConfigKeys.FIELDS not in cfg


# ── Custom WIQL subcard ────────────────────────────────────────────────────────

def test_wi_custom_query_stored():
    cfg = _wi_custom(ConfigQuery="SELECT [System.Id] FROM WorkItems WHERE [System.State] = 'Active'")
    assert cfg[ConfigKeys.QUERY] == "SELECT [System.Id] FROM WorkItems WHERE [System.State] = 'Active'"


def test_wi_custom_fields_stored():
    cfg = _wi_custom(ConfigQuery="SELECT ...", ConfigFields="System.Title,System.Description,System.State")
    assert cfg[ConfigKeys.FIELDS] == "System.Title,System.Description,System.State"


def test_wi_custom_fields_absent_when_blank():
    # User left Fields empty (blank = fetch all) → not submitted → not in config
    cfg = _wi_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.FIELDS not in cfg


def test_wi_custom_item_types_absent():
    # Custom WIQL tab active → browser does not submit ConfigItemTypes checkboxes
    cfg = _wi_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.ITEM_TYPES not in cfg


def test_wi_custom_area_path_absent():
    # Custom WIQL tab active → browser does not submit ConfigAreaPath
    cfg = _wi_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.AREA_PATH not in cfg


def test_wi_custom_iteration_path_absent():
    # Custom WIQL tab active → browser does not submit ConfigIterationPath
    cfg = _wi_custom(ConfigQuery="SELECT ...")
    assert ConfigKeys.ITERATION_PATH not in cfg


# ── ADO connection card ────────────────────────────────────────────────────────

def test_wi_ado_base_url_stored():
    cfg = _wi_filters(ConfigBaseUrl="https://dev.azure.com/myorg/myproject",
                      **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_wi_ado_api_version_stored():
    cfg = _wi_filters(ConfigApiVersion="6.0", **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.API_VERSION] == "6.0"


def test_wi_verify_ssl_stored():
    cfg = _wi_filters(ConfigVerifySSL="false", **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.VERIFY_SSL] == "false"


# ── Auth type subcards (ADO connection) ───────────────────────────────────────
# The auth section uses display:none (not disabled) so the browser always sends
# ALL auth field values.  The parser gates each credential on the selected auth type.

def test_wi_auth_pat_stores_pat_only():
    cfg = _wi_filters(ConfigAuthType="pat",
                      ConfigPat="TFS_PAT",
                      ConfigToken="SHOULD_BE_IGNORED",
                      **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.PAT] == "TFS_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_wi_auth_bearer_stores_token_only():
    cfg = _wi_filters(ConfigAuthType="bearer",
                      ConfigPat="SHOULD_BE_IGNORED",
                      ConfigToken="MY_BEARER_TOKEN",
                      **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_wi_auth_ntlm_stores_windows_credentials():
    cfg = _wi_filters(ConfigAuthType="ntlm",
                      ConfigUsername="CORP\\svc-account",
                      ConfigPassword="MY_PASSWORD",
                      ConfigDomain="CORP",
                      **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc-account"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_wi_auth_negotiate_stores_windows_credentials():
    cfg = _wi_filters(ConfigAuthType="negotiate",
                      ConfigUsername="user",
                      ConfigPassword="pass",
                      ConfigDomain="DOM",
                      **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_wi_auth_apikey_stores_header_and_value():
    cfg = _wi_filters(ConfigAuthType="apikey",
                      ConfigApiKeyHeader="X-Api-Key",
                      ConfigApiKeyValue="MY_SECRET_KEY",
                      **{"ConfigItemTypes": ["Bug"]})
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET_KEY"


def test_wi_auth_none_stores_no_credentials():
    cfg = _wi_filters(ConfigAuthType="none",
                      ConfigPat="ignored", ConfigToken="ignored",
                      ConfigUsername="ignored", ConfigPassword="ignored",
                      ConfigApiKeyValue="ignored",
                      **{"ConfigItemTypes": ["Bug"]})
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_wi_auth_ntlm_does_not_store_pat():
    cfg = _wi_filters(ConfigAuthType="ntlm",
                      ConfigPat="SHOULD_BE_IGNORED",
                      ConfigUsername="u", ConfigPassword="p", ConfigDomain="d",
                      **{"ConfigItemTypes": ["Bug"]})
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with work-item source type ───────────────────────

def test_wi_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/workitems",
               ConfigHttpMethod="GET")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/workitems"
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_wi_custom_provider_does_not_store_ado_type_fields():
    # sysTab('custom') disables ALL inputs inside ado-provider-content, so the
    # browser submits only custom-API fields.  No ADO fields reach the server.
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/workitems")
    assert ConfigKeys.ITEM_TYPES not in cfg
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.QUERY not in cfg


# ── Provider tab: Manual with work-item source type ───────────────────────────

def test_wi_manual_provider_stores_content():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Backlog Export",
               ConfigManualText="Work item content pasted here")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.CONTENT] == "Work item content pasted here"
    assert cfg[ConfigKeys.TITLE] == "Backlog Export"


def test_wi_manual_provider_does_not_store_ado_type_fields():
    # sysTab('manual') disables ALL inputs inside ado-provider-content, so the
    # browser submits only manual fields.  No ADO fields reach the server.
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="content")
    assert ConfigKeys.ITEM_TYPES not in cfg
    assert ConfigKeys.AREA_PATH not in cfg
    assert ConfigKeys.QUERY not in cfg
