"""
Tests for _build_source_from_form – Pipeline Build source type.

UI layout: two subcards inside the ADO provider card.

  [Build Pipeline tab]    ConfigPipelineId, ConfigLastNBuilds
  [Release Pipeline tab]  ConfigReleaseDefinitionId, ConfigLastNReleases

ConfigBuildType is a real hidden field (always submitted); buildTab() updates
its value and disables the inactive panel's inputs.

Template pre-fills: LastNBuilds="5", LastNReleases="5"
Server defaults:    BuildType="build", LastNBuilds="5", LastNReleases="5"

Cross-subcard leakage when non-ADO provider:
  ConfigBuildType is disabled → server defaults to "build"
  → LastNBuilds="5" also defaults in (no ConfigPipelineId since no default)
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helpers ───────────────────────────────────────────────────────────────────

def _build_build(**fields) -> dict:
    """Simulates Build Pipeline tab active: ConfigBuildType=build, release panel disabled."""
    return _cfg(SourceTypes.PIPELINE_BUILD, ConfigBuildType="build", **fields)


def _build_release(**fields) -> dict:
    """Simulates Release Pipeline tab active: ConfigBuildType=release, build panel disabled."""
    return _cfg(SourceTypes.PIPELINE_BUILD, ConfigBuildType="release", **fields)


# ── ConfigBuildType hidden field ──────────────────────────────────────────────

def test_build_type_defaults_to_build_when_missing():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD)
    assert cfg[ConfigKeys.BUILD_TYPE] == "build"


def test_build_type_build_stored():
    cfg = _build_build(ConfigPipelineId="42")
    assert cfg[ConfigKeys.BUILD_TYPE] == "build"


def test_build_type_release_stored():
    cfg = _build_release(ConfigReleaseDefinitionId="7")
    assert cfg[ConfigKeys.BUILD_TYPE] == "release"


# ── Build Pipeline subcard ────────────────────────────────────────────────────

def test_build_pipeline_id_stored():
    cfg = _build_build(ConfigPipelineId="42")
    assert cfg[ConfigKeys.PIPELINE_ID] == "42"


def test_build_pipeline_id_absent_when_empty():
    cfg = _build_build(ConfigPipelineId="")
    assert ConfigKeys.PIPELINE_ID not in cfg


def test_build_pipeline_id_absent_when_not_submitted():
    cfg = _build_build()
    assert ConfigKeys.PIPELINE_ID not in cfg


def test_build_last_n_builds_stored():
    cfg = _build_build(ConfigPipelineId="42", ConfigLastNBuilds="10")
    assert cfg[ConfigKeys.LAST_N_BUILDS] == "10"


def test_build_last_n_builds_template_prefill_five():
    cfg = _build_build(ConfigPipelineId="42", ConfigLastNBuilds="5")
    assert cfg[ConfigKeys.LAST_N_BUILDS] == "5"


def test_build_last_n_builds_server_default_when_absent():
    cfg = _build_build(ConfigPipelineId="42")
    assert cfg[ConfigKeys.LAST_N_BUILDS] == "5"


def test_build_last_n_builds_min_boundary():
    cfg = _build_build(ConfigPipelineId="42", ConfigLastNBuilds="1")
    assert cfg[ConfigKeys.LAST_N_BUILDS] == "1"


def test_build_last_n_builds_max_boundary():
    cfg = _build_build(ConfigPipelineId="42", ConfigLastNBuilds="50")
    assert cfg[ConfigKeys.LAST_N_BUILDS] == "50"


def test_build_pipeline_all_fields_together():
    cfg = _build_build(ConfigPipelineId="99", ConfigLastNBuilds="20")
    assert cfg[ConfigKeys.BUILD_TYPE] == "build"
    assert cfg[ConfigKeys.PIPELINE_ID] == "99"
    assert cfg[ConfigKeys.LAST_N_BUILDS] == "20"


# ── Build subcard: release panel inputs absent (browser disabled them) ─────────

def test_build_release_definition_id_absent_when_build_tab_active():
    cfg = _build_build(ConfigPipelineId="42")
    assert ConfigKeys.RELEASE_DEFINITION_ID not in cfg


def test_build_last_n_releases_absent_when_build_tab_active():
    cfg = _build_build(ConfigPipelineId="42")
    assert ConfigKeys.LAST_N_RELEASES not in cfg


# ── Release Pipeline subcard ──────────────────────────────────────────────────

def test_build_release_definition_id_stored():
    cfg = _build_release(ConfigReleaseDefinitionId="7")
    assert cfg[ConfigKeys.RELEASE_DEFINITION_ID] == "7"


def test_build_release_definition_id_absent_when_empty():
    cfg = _build_release(ConfigReleaseDefinitionId="")
    assert ConfigKeys.RELEASE_DEFINITION_ID not in cfg


def test_build_release_definition_id_absent_when_not_submitted():
    cfg = _build_release()
    assert ConfigKeys.RELEASE_DEFINITION_ID not in cfg


def test_build_last_n_releases_stored():
    cfg = _build_release(ConfigReleaseDefinitionId="7", ConfigLastNReleases="3")
    assert cfg[ConfigKeys.LAST_N_RELEASES] == "3"


def test_build_last_n_releases_template_prefill_five():
    cfg = _build_release(ConfigReleaseDefinitionId="7", ConfigLastNReleases="5")
    assert cfg[ConfigKeys.LAST_N_RELEASES] == "5"


def test_build_last_n_releases_server_default_when_absent():
    cfg = _build_release(ConfigReleaseDefinitionId="7")
    assert cfg[ConfigKeys.LAST_N_RELEASES] == "5"


def test_build_last_n_releases_min_boundary():
    cfg = _build_release(ConfigReleaseDefinitionId="7", ConfigLastNReleases="1")
    assert cfg[ConfigKeys.LAST_N_RELEASES] == "1"


def test_build_last_n_releases_max_boundary():
    cfg = _build_release(ConfigReleaseDefinitionId="7", ConfigLastNReleases="50")
    assert cfg[ConfigKeys.LAST_N_RELEASES] == "50"


def test_build_release_all_fields_together():
    cfg = _build_release(ConfigReleaseDefinitionId="12", ConfigLastNReleases="8")
    assert cfg[ConfigKeys.BUILD_TYPE] == "release"
    assert cfg[ConfigKeys.RELEASE_DEFINITION_ID] == "12"
    assert cfg[ConfigKeys.LAST_N_RELEASES] == "8"


# ── Release subcard: build panel inputs absent (browser disabled them) ─────────

def test_build_pipeline_id_absent_when_release_tab_active():
    cfg = _build_release(ConfigReleaseDefinitionId="7")
    assert ConfigKeys.PIPELINE_ID not in cfg


def test_build_last_n_builds_absent_when_release_tab_active():
    # build_type="release" → build branch of parser never runs → no LastNBuilds default
    cfg = _build_release(ConfigReleaseDefinitionId="7")
    assert ConfigKeys.LAST_N_BUILDS not in cfg


# ── ADO connection card ────────────────────────────────────────────────────────

def test_build_ado_base_url_stored():
    cfg = _build_build(ConfigBaseUrl="https://dev.azure.com/myorg/myproject",
                       ConfigPipelineId="42")
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_build_ado_api_version_stored():
    cfg = _build_build(ConfigApiVersion="7.1", ConfigPipelineId="42")
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_build_verify_ssl_stored():
    cfg = _build_build(ConfigVerifySSL="false", ConfigPipelineId="42")
    assert cfg[ConfigKeys.VERIFY_SSL] == "false"


# ── Auth type subcards (ADO connection) ───────────────────────────────────────

def test_build_auth_pat_stores_pat_only():
    cfg = _build_build(ConfigPipelineId="42",
                       ConfigAuthType="pat",
                       ConfigPat="TFS_PAT",
                       ConfigToken="SHOULD_BE_IGNORED")
    assert cfg[ConfigKeys.PAT] == "TFS_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_build_auth_bearer_stores_token_only():
    cfg = _build_build(ConfigPipelineId="42",
                       ConfigAuthType="bearer",
                       ConfigPat="SHOULD_BE_IGNORED",
                       ConfigToken="MY_BEARER_TOKEN")
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_build_auth_ntlm_stores_windows_credentials():
    cfg = _build_build(ConfigPipelineId="42",
                       ConfigAuthType="ntlm",
                       ConfigUsername="CORP\\svc",
                       ConfigPassword="MY_PASSWORD",
                       ConfigDomain="CORP")
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_build_auth_negotiate_stores_windows_credentials():
    cfg = _build_build(ConfigPipelineId="42",
                       ConfigAuthType="negotiate",
                       ConfigUsername="user",
                       ConfigPassword="pass",
                       ConfigDomain="DOM")
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_build_auth_apikey_stores_header_and_value():
    cfg = _build_build(ConfigPipelineId="42",
                       ConfigAuthType="apikey",
                       ConfigApiKeyHeader="X-Api-Key",
                       ConfigApiKeyValue="MY_SECRET_KEY")
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET_KEY"


def test_build_auth_none_stores_no_credentials():
    cfg = _build_build(ConfigPipelineId="42",
                       ConfigAuthType="none",
                       ConfigPat="ignored", ConfigToken="ignored",
                       ConfigUsername="ignored", ConfigPassword="ignored",
                       ConfigApiKeyValue="ignored")
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_build_auth_ntlm_does_not_store_pat():
    cfg = _build_build(ConfigPipelineId="42",
                       ConfigAuthType="ntlm",
                       ConfigPat="SHOULD_BE_IGNORED",
                       ConfigUsername="u", ConfigPassword="p", ConfigDomain="d")
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with pipeline-build source type ──────────────────

def test_build_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/builds",
               ConfigHttpMethod="GET")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/builds"
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_build_custom_provider_stores_response_mapping():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/builds",
               ConfigItemsPath="value",
               ConfigTitleField="name",
               ConfigContentFields="logs,result")
    assert cfg[ConfigKeys.ITEMS_PATH] == "value"
    assert cfg[ConfigKeys.TITLE_FIELD] == "name"
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "logs,result"


def test_build_custom_provider_pipeline_id_absent():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/builds")
    assert ConfigKeys.PIPELINE_ID not in cfg


def test_build_custom_provider_build_type_and_last_n_builds_server_defaults_leak():
    # ConfigBuildType disabled in browser → server defaults "build" → LastNBuilds="5"
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/builds")
    assert cfg.get(ConfigKeys.BUILD_TYPE) == "build"
    assert cfg.get(ConfigKeys.LAST_N_BUILDS) == "5"


# ── Provider tab: Manual with pipeline-build source type ─────────────────────

def test_build_manual_provider_text_stores_content_and_title():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Build Logs",
               ConfigManualText="Build output pasted here")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "Build output pasted here"
    assert cfg[ConfigKeys.TITLE] == "Build Logs"


def test_build_manual_provider_upload_carries_existing_content():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualExistingContent="Previously uploaded build logs")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded build logs"


def test_build_manual_provider_pipeline_id_absent():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="content")
    assert ConfigKeys.PIPELINE_ID not in cfg
    assert ConfigKeys.RELEASE_DEFINITION_ID not in cfg


def test_build_manual_provider_build_type_server_default_leaks():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="content")
    assert cfg.get(ConfigKeys.BUILD_TYPE) == "build"
    assert cfg.get(ConfigKeys.LAST_N_BUILDS) == "5"


def test_pipeline_build_does_not_store_release_fields_when_build():
    cfg = _cfg(SourceTypes.PIPELINE_BUILD,
               ConfigBuildType="build",
               ConfigPipelineId="42",
               ConfigReleaseDefinitionId="99")
    assert ConfigKeys.RELEASE_DEFINITION_ID not in cfg
