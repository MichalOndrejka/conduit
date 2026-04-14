"""
Tests for _build_source_from_form – identity fields, provider defaults,
auth type subcards, generic custom-API/manual provider behaviour, and
cross-provider isolation.  These tests are source-type-agnostic.
"""
from app.models import ConfigKeys, SourceTypes
from app.web.routes import _build_source_from_form
from form_helpers import MockForm, _cfg, _form


# ── Identity fields ───────────────────────────────────────────────────────────

def test_name_is_stored():
    form = _form(source_type=SourceTypes.CODE_REPO, name="My Source",
                 ConfigRepository="Repo", ConfigBranch="main")
    src = _build_source_from_form(form)
    assert src.name == "My Source"


def test_source_type_is_stored():
    form = _form(source_type=SourceTypes.CODE_REPO, name="n",
                 ConfigRepository="Repo", ConfigBranch="main")
    src = _build_source_from_form(form)
    assert src.type == SourceTypes.CODE_REPO


def test_explicit_source_id_is_preserved():
    form = _form(**{"source_type": SourceTypes.CODE_REPO, "name": "n",
                    "Source.Id": "fixed-id",
                    "ConfigRepository": "Repo", "ConfigBranch": "main"})
    src = _build_source_from_form(form)
    assert src.id == "fixed-id"


def test_missing_source_id_generates_uuid():
    form = _form(source_type=SourceTypes.CODE_REPO, name="n",
                 ConfigRepository="Repo", ConfigBranch="main")
    src = _build_source_from_form(form)
    assert len(src.id) == 36


# ── Provider tab: ADO (default) ───────────────────────────────────────────────

def test_provider_defaults_to_ado():
    cfg = _cfg(SourceTypes.TEST_RESULTS)
    assert cfg[ConfigKeys.PROVIDER] == "ado"


def test_provider_ado_explicit():
    cfg = _cfg(SourceTypes.TEST_RESULTS, ConfigProvider="ado")
    assert cfg[ConfigKeys.PROVIDER] == "ado"


def test_ado_connection_base_url():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigBaseUrl="https://dev.azure.com/org",
               ConfigAuthType="none")
    assert cfg[ConfigKeys.BASE_URL] == "https://dev.azure.com/org"


def test_ado_connection_api_version():
    cfg = _cfg(SourceTypes.TEST_RESULTS, ConfigApiVersion="7.1")
    assert cfg[ConfigKeys.API_VERSION] == "7.1"


def test_ado_connection_verify_ssl_stored():
    cfg = _cfg(SourceTypes.TEST_RESULTS, ConfigVerifySSL="false")
    assert cfg[ConfigKeys.VERIFY_SSL] == "false"


def test_ado_connection_verify_ssl_custom_bundle():
    cfg = _cfg(SourceTypes.TEST_RESULTS, ConfigVerifySSL="/etc/ssl/certs/ca.pem")
    assert cfg[ConfigKeys.VERIFY_SSL] == "/etc/ssl/certs/ca.pem"


# ── Auth type subcards ────────────────────────────────────────────────────────

def test_auth_pat_stores_pat():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigAuthType="pat", ConfigPat="my-pat-token")
    assert cfg[ConfigKeys.AUTH_TYPE] == "pat"
    assert cfg[ConfigKeys.PAT] == "my-pat-token"


def test_auth_pat_does_not_store_token():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigAuthType="pat", ConfigPat="p", ConfigToken="t")
    assert ConfigKeys.TOKEN not in cfg


def test_auth_bearer_stores_token():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigAuthType="bearer", ConfigToken="bearer-tok")
    assert cfg[ConfigKeys.AUTH_TYPE] == "bearer"
    assert cfg[ConfigKeys.TOKEN] == "bearer-tok"


def test_auth_bearer_does_not_store_pat():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigAuthType="bearer", ConfigToken="t", ConfigPat="p")
    assert ConfigKeys.PAT not in cfg


def test_auth_ntlm_stores_credentials():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigAuthType="ntlm",
               ConfigUsername="CORP\\svc",
               ConfigPassword="secret",
               ConfigDomain="CORP")
    assert cfg[ConfigKeys.AUTH_TYPE] == "ntlm"
    assert cfg[ConfigKeys.USERNAME] == "CORP\\svc"
    assert cfg[ConfigKeys.PASSWORD] == "secret"
    assert cfg[ConfigKeys.DOMAIN] == "CORP"


def test_auth_negotiate_stores_credentials():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigAuthType="negotiate",
               ConfigUsername="user",
               ConfigPassword="pass",
               ConfigDomain="DOM")
    assert cfg[ConfigKeys.AUTH_TYPE] == "negotiate"
    assert cfg[ConfigKeys.USERNAME] == "user"
    assert cfg[ConfigKeys.PASSWORD] == "pass"


def test_auth_apikey_stores_header_and_value():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigAuthType="apikey",
               ConfigApiKeyHeader="X-Api-Key",
               ConfigApiKeyValue="abc123")
    assert cfg[ConfigKeys.API_KEY_HEADER] == "X-Api-Key"
    assert cfg[ConfigKeys.API_KEY_VALUE] == "abc123"


def test_auth_none_stores_no_credentials():
    cfg = _cfg(SourceTypes.TEST_RESULTS, ConfigAuthType="none")
    for key in (ConfigKeys.PAT, ConfigKeys.TOKEN, ConfigKeys.USERNAME,
                ConfigKeys.PASSWORD, ConfigKeys.API_KEY_VALUE):
        assert key not in cfg


# ── Provider tab: Custom API (generic behaviour) ──────────────────────────────

def test_custom_provider_stored():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/items")
    assert cfg[ConfigKeys.PROVIDER] == "custom"


def test_custom_api_stores_url():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://api.example.com/items")
    assert cfg[ConfigKeys.URL] == "https://api.example.com/items"


def test_custom_api_stores_http_method():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://x",
               ConfigHttpMethod="POST")
    assert cfg[ConfigKeys.HTTP_METHOD] == "POST"


def test_custom_api_http_method_default():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://x")
    assert cfg[ConfigKeys.HTTP_METHOD] == "GET"


def test_custom_api_stores_items_path():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://x",
               ConfigItemsPath="data.items")
    assert cfg[ConfigKeys.ITEMS_PATH] == "data.items"


def test_custom_api_stores_title_field():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://x",
               ConfigTitleField="name")
    assert cfg[ConfigKeys.TITLE_FIELD] == "name"


def test_custom_api_title_field_default():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://x")
    assert cfg[ConfigKeys.TITLE_FIELD] == "title"


def test_custom_api_stores_content_fields():
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://x",
               ConfigContentFields="description,body")
    assert cfg[ConfigKeys.CONTENT_FIELDS] == "description,body"


def test_custom_api_does_not_store_ado_auth_fields():
    # Auth parsing is provider-agnostic — PAT IS stored even for custom provider.
    # This test documents the current behaviour as a regression anchor.
    cfg = _cfg(SourceTypes.WORK_ITEM_QUERY,
               ConfigProvider="custom",
               ConfigUrl="https://x",
               ConfigAuthType="pat",
               ConfigPat="some-pat")
    assert cfg[ConfigKeys.PAT] == "some-pat"


# ── Provider tab: Manual (generic behaviour) ──────────────────────────────────

def test_manual_provider_stored():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="My Notes",
               ConfigManualText="Some content here")
    assert cfg[ConfigKeys.PROVIDER] == "manual"


def test_manual_text_type_stores_content():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="Notes",
               ConfigManualText="Hello world")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"
    assert cfg[ConfigKeys.CONTENT] == "Hello world"
    assert cfg[ConfigKeys.TITLE] == "Notes"


def test_manual_text_type_default():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualText="content")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "text"


def test_manual_upload_type_carries_existing_content():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="upload",
               ConfigManualTitle="Spec",
               ConfigManualExistingContent="Previously uploaded text")
    assert cfg[ConfigKeys.MANUAL_TYPE] == "upload"
    assert cfg[ConfigKeys.CONTENT] == "Previously uploaded text"


def test_manual_upload_type_no_content_when_no_existing():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="upload")
    assert ConfigKeys.CONTENT not in cfg


def test_manual_text_does_not_use_existing_content():
    # Text mode reads ConfigManualText, not ConfigManualExistingContent
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualText="Real content",
               ConfigManualExistingContent="Old content")
    assert cfg[ConfigKeys.CONTENT] == "Real content"


def test_manual_stores_title():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="manual",
               ConfigManualType="text",
               ConfigManualTitle="My Title",
               ConfigManualText="body")
    assert cfg[ConfigKeys.TITLE] == "My Title"


# ── Cross-provider isolation ──────────────────────────────────────────────────

def test_ado_provider_does_not_store_custom_api_fields():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="ado",
               ConfigUrl="https://should-be-ignored")
    assert ConfigKeys.URL not in cfg


def test_ado_provider_does_not_store_manual_fields():
    cfg = _cfg(SourceTypes.TEST_RESULTS,
               ConfigProvider="ado",
               ConfigManualText="should-be-ignored")
    assert ConfigKeys.CONTENT not in cfg


def test_custom_provider_does_not_store_manual_fields():
    cfg = _cfg(SourceTypes.DOCUMENTATION,
               ConfigProvider="custom",
               ConfigUrl="https://x",
               ConfigManualText="should-be-ignored")
    assert ConfigKeys.CONTENT not in cfg
