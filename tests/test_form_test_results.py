"""
Tests for _build_source_from_form – Test Results source type.

UI layout: single "Filters" tab (no subcard switching, no hidden mode key).

  ConfigLastNRuns      template pre-fill: "10",  server default: "10"
  ConfigResultsPerRun  template pre-fill: "200", server default: "200"

Server-default leakage: both fields default in for non-ADO providers since
the TEST_RESULTS type block runs unconditionally regardless of provider.
"""
from app.models import ConfigKeys, SourceTypes
from form_helpers import _cfg


# ── helper ────────────────────────────────────────────────────────────────────

def _tr(**fields) -> dict:
    """Convenience wrapper for test-results _cfg calls."""
    return _cfg(SourceTypes.TEST_RESULTS, **fields)


# ── LastNRuns field ───────────────────────────────────────────────────────────

def test_tr_last_n_runs_stored():
    cfg = _tr(ConfigLastNRuns="20")
    assert cfg[ConfigKeys.LAST_N_RUNS] == "20"


def test_tr_last_n_runs_server_default_when_absent():
    cfg = _tr()
    assert cfg[ConfigKeys.LAST_N_RUNS] == "10"


def test_tr_last_n_runs_server_default_when_empty():
    # Empty string is falsy in _set → server default "10" fires
    cfg = _tr(ConfigLastNRuns="")
    assert cfg[ConfigKeys.LAST_N_RUNS] == "10"


def test_tr_last_n_runs_template_prefill_ten():
    # Template pre-fills value="10"; submitted as-is is identical to the default
    cfg = _tr(ConfigLastNRuns="10")
    assert cfg[ConfigKeys.LAST_N_RUNS] == "10"


def test_tr_last_n_runs_min_boundary():
    cfg = _tr(ConfigLastNRuns="1")
    assert cfg[ConfigKeys.LAST_N_RUNS] == "1"


def test_tr_last_n_runs_max_boundary():
    cfg = _tr(ConfigLastNRuns="100")
    assert cfg[ConfigKeys.LAST_N_RUNS] == "100"


# ── ResultsPerRun field ───────────────────────────────────────────────────────

def test_tr_results_per_run_stored():
    cfg = _tr(ConfigResultsPerRun="500")
    assert cfg[ConfigKeys.RESULTS_PER_RUN] == "500"


def test_tr_results_per_run_server_default_when_absent():
    cfg = _tr()
    assert cfg[ConfigKeys.RESULTS_PER_RUN] == "200"


def test_tr_results_per_run_server_default_when_empty():
    cfg = _tr(ConfigResultsPerRun="")
    assert cfg[ConfigKeys.RESULTS_PER_RUN] == "200"


def test_tr_results_per_run_template_prefill_two_hundred():
    cfg = _tr(ConfigResultsPerRun="200")
    assert cfg[ConfigKeys.RESULTS_PER_RUN] == "200"


def test_tr_results_per_run_min_boundary():
    cfg = _tr(ConfigResultsPerRun="1")
    assert cfg[ConfigKeys.RESULTS_PER_RUN] == "1"


def test_tr_results_per_run_max_boundary():
    cfg = _tr(ConfigResultsPerRun="1000")
    assert cfg[ConfigKeys.RESULTS_PER_RUN] == "1000"


# ── Both fields together ──────────────────────────────────────────────────────

def test_tr_both_fields_stored():
    cfg = _tr(ConfigLastNRuns="30", ConfigResultsPerRun="750")
    assert cfg[ConfigKeys.LAST_N_RUNS] == "30"
    assert cfg[ConfigKeys.RESULTS_PER_RUN] == "750"


# ── ADO connection fields ─────────────────────────────────────────────────────

def test_tr_ado_base_url_stored():
    cfg = _tr(ConfigBaseUrl="https://dev.azure.com/myorg/myproject",
              ConfigLastNRuns="10")
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/myorg/myproject"


def test_tr_ado_api_version_stored():
    cfg = _tr(ConfigApiVersion="7.1", ConfigLastNRuns="10")
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_tr_verify_ssl_not_stored():
    cfg = _tr(ConfigVerifySSL="false", ConfigLastNRuns="10")
    assert ConfigKeys.VERIFY_SSL not in cfg  # BUG: VerifySSL is silently dropped


# ── Auth type subcards (ADO connection) ───────────────────────────────────────

def test_tr_auth_pat_stores_pat_only():
    cfg = _tr(ConfigAuthType="pat",
              ConfigPat="MY_PAT",
              ConfigToken="SHOULD_BE_IGNORED")
    assert cfg[ConfigKeys.PAT] == "MY_PAT"
    assert ConfigKeys.TOKEN not in cfg


def test_tr_auth_bearer_stores_token_only():
    cfg = _tr(ConfigAuthType="bearer",
              ConfigPat="SHOULD_BE_IGNORED",
              ConfigToken="MY_BEARER_TOKEN")
    assert cfg[ConfigKeys.TOKEN] == "MY_BEARER_TOKEN"
    assert ConfigKeys.PAT not in cfg


def test_tr_auth_ntlm_stores_windows_credentials():
    cfg = _tr(ConfigAuthType="ntlm",
              ConfigUsername="CORP\\svc",
              ConfigPassword="MY_PASSWORD",
              ConfigDomain="CORP")
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc"
    assert cfg[ConfigKeys.PASSWORD] == "MY_PASSWORD"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_tr_auth_negotiate_stores_windows_credentials():
    cfg = _tr(ConfigAuthType="negotiate",
              ConfigUsername="user",
              ConfigPassword="pass",
              ConfigDomain="DOM")
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"
    assert cfg[ConfigKeys.DOMAIN] == "DOM"


def test_tr_auth_apikey_stores_header_and_value():
    cfg = _tr(ConfigAuthType="apikey",
              ConfigApiKeyHeader="X-Api-Key",
              ConfigApiKeyValue="MY_SECRET")
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "MY_SECRET"


def test_tr_auth_none_stores_no_credentials():
    cfg = _tr(ConfigAuthType="none",
              ConfigPat="ignored", ConfigToken="ignored",
              ConfigUsername="ignored", ConfigPassword="ignored",
              ConfigApiKeyValue="ignored")
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


def test_tr_auth_ntlm_does_not_store_pat():
    cfg = _tr(ConfigAuthType="ntlm",
              ConfigPat="SHOULD_BE_IGNORED",
              ConfigUsername="u", ConfigPassword="p", ConfigDomain="d")
    assert ConfigKeys.PAT not in cfg


# ── Provider tab: Custom API with test-results source type ────────────────────
# sysTab('custom') disables ado-provider-content: ConfigLastNRuns and
# ConfigResultsPerRun are disabled → server defaults "10" and "200" leak in.

def test_tr_custom_provider_stores_url_and_method():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testresults",
               ConfigHttpMethod="GET")
    assert cfg[ConfigKeys.PROVIDER] == "custom"
    assert cfg[ConfigKeys.URL] == "https://api.example.com/testresults"
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_tr_custom_provider_stores_response_mapping():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testresults",
               ConfigItemsPath="value",
               ConfigTitleField="testName",
               ConfigContentFields="outcome,duration")
    assert cfg[ConfigKeys.ITEMS_PATH] == "value"
    assert cfg[ConfigKeys.TITLE_FIELD] == "testName"
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "outcome,duration"


def test_tr_custom_provider_last_n_runs_server_default_leaks():
    # ConfigLastNRuns disabled in browser → server defaults "10"
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testresults")
    assert cfg.get(ConfigKeys.LAST_N_RUNS) == "10"


def test_tr_custom_provider_results_per_run_server_default_leaks():
    # ConfigResultsPerRun disabled in browser → server defaults "200"
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testresults")
    assert cfg.get(ConfigKeys.RESULTS_PER_RUN) == "200"


def test_tr_custom_provider_no_ado_connection_fields():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/testresults")
    assert ConfigKeys.BASE_URL not in cfg
    assert ConfigKeys.API_VERSION not in cfg


# ── Provider tab: Manual with test-results source type ───────────────────────

def test_tr_manual_provider_text_stores_content_and_title():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Test Run Summary",
               ConfigManualText="All tests passed")
    assert cfg[ConfigKeys.PROVIDER] == "manual"
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "All tests passed"
    assert cfg[ConfigKeys.TITLE] == "Test Run Summary"


def test_tr_manual_provider_upload_carries_existing_content():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualExistingContent="Previously uploaded test results")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded test results"


def test_tr_manual_provider_last_n_runs_server_default_leaks():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="results")
    assert cfg.get(ConfigKeys.LAST_N_RUNS) == "10"


def test_tr_manual_provider_results_per_run_server_default_leaks():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="results")
    assert cfg.get(ConfigKeys.RESULTS_PER_RUN) == "200"


def test_tr_manual_provider_no_ado_connection_fields():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="results")
    assert ConfigKeys.BASE_URL not in cfg
    assert ConfigKeys.API_VERSION not in cfg
