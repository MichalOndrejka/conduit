"""
Tests for AdoConnection.from_config, _url, and _check_html_auth_redirect.
No network calls are made.
"""
from unittest.mock import MagicMock

import pytest

from app.ado.client import AdoConnection, _check_html_auth_redirect


# ── from_config field mapping ─────────────────────────────────────────────────

def test_from_config_maps_base_url():
    conn = AdoConnection.from_config({"BaseUrl": "https://ado.example.com/proj"})
    assert conn.base_url == "https://ado.example.com/proj"


def test_from_config_strips_trailing_slash():
    conn = AdoConnection.from_config({"BaseUrl": "https://ado.example.com/proj/"})
    assert not conn.base_url.endswith("/")


def test_from_config_maps_auth_type():
    conn = AdoConnection.from_config({"AuthType": "pat"})
    assert conn.auth_type == "pat"


def test_from_config_default_auth_type_is_none():
    conn = AdoConnection.from_config({})
    assert conn.auth_type == "none"


def test_from_config_default_api_version():
    conn = AdoConnection.from_config({})
    assert conn.api_version == "7.1"


def test_from_config_maps_pat():
    conn = AdoConnection.from_config({"Pat": "MY_PAT_VAR"})
    assert conn.pat == "MY_PAT_VAR"


def test_from_config_maps_token():
    conn = AdoConnection.from_config({"Token": "MY_TOKEN_VAR"})
    assert conn.token == "MY_TOKEN_VAR"


def test_from_config_maps_username_and_password():
    conn = AdoConnection.from_config({"Username": "alice", "Password": "MY_PW_VAR"})
    assert conn.username == "alice"
    assert conn.password == "MY_PW_VAR"


def test_from_config_maps_domain():
    conn = AdoConnection.from_config({"Domain": "CORP"})
    assert conn.domain == "CORP"


def test_from_config_maps_api_key_header_and_value():
    conn = AdoConnection.from_config({"ApiKeyHeader": "X-Api-Key", "ApiKeyValue": "MY_KEY_VAR"})
    assert conn.api_key_header == "X-Api-Key"
    assert conn.api_key_value == "MY_KEY_VAR"


def test_from_config_empty_dict_produces_valid_connection():
    conn = AdoConnection.from_config({})
    assert conn.base_url == ""
    assert conn.pat == ""


# ── _url ──────────────────────────────────────────────────────────────────────

def test_url_raises_value_error_when_base_url_empty():
    conn = AdoConnection(base_url="")
    with pytest.raises(ValueError, match="BaseUrl"):
        conn._url("_apis/wit/wiql")


def test_url_appends_api_version_query_param():
    conn = AdoConnection(base_url="https://example.com/proj")
    url = conn._url("_apis/something")
    assert "api-version=" in url


def test_url_strips_leading_slash_from_path():
    conn = AdoConnection(base_url="https://example.com/proj")
    url_with = conn._url("/_apis/something")
    url_without = conn._url("_apis/something")
    # Both should resolve to the same URL — no double-slash
    assert "///" not in url_with
    assert url_with == url_without


def test_url_includes_extra_params():
    conn = AdoConnection(base_url="https://example.com/proj")
    url = conn._url("_apis/build/builds", **{"$top": "5"})
    assert "$top=5" in url


def test_url_uses_custom_api_version():
    conn = AdoConnection(base_url="https://example.com/proj", api_version="6.0")
    url = conn._url("_apis/something")
    assert "api-version=6.0" in url


# ── _check_html_auth_redirect ─────────────────────────────────────────────────

def _resp(status: int = 200, content_type: str = "application/json", text: str = "{}") -> MagicMock:
    r = MagicMock()
    r.status_code = status
    r.headers = {"Content-Type": content_type}
    r.text = text
    return r


def test_html_content_type_raises():
    with pytest.raises(ValueError, match="sign-in"):
        _check_html_auth_redirect(_resp(content_type="text/html"), "http://x", "GET")


def test_xhtml_content_type_raises():
    with pytest.raises(ValueError):
        _check_html_auth_redirect(_resp(content_type="application/xhtml+xml"), "http://x", "GET")


def test_doctype_body_raises():
    with pytest.raises(ValueError, match="sign-in"):
        _check_html_auth_redirect(_resp(text="<!DOCTYPE html><html>..."), "http://x", "GET")


def test_html_tag_body_raises():
    with pytest.raises(ValueError):
        _check_html_auth_redirect(_resp(text="<html><body>Sign in</body></html>"), "http://x", "GET")


def test_json_response_does_not_raise():
    # Should complete without raising
    _check_html_auth_redirect(_resp(text='{"value": []}'), "http://x", "GET")


def test_empty_body_does_not_raise():
    _check_html_auth_redirect(_resp(text=""), "http://x", "POST")


# ── _resolve_env ──────────────────────────────────────────────────────────────

def test_resolve_env_returns_env_var_value_when_set(monkeypatch):
    monkeypatch.setenv("MY_SECRET_VAR", "actual-secret")
    conn = AdoConnection(base_url="http://x")
    assert conn._resolve_env("MY_SECRET_VAR") == "actual-secret"


def test_resolve_env_falls_back_to_literal_when_var_not_set(monkeypatch):
    monkeypatch.delenv("UNSET_VAR_XYZ", raising=False)
    conn = AdoConnection(base_url="http://x")
    assert conn._resolve_env("UNSET_VAR_XYZ") == "UNSET_VAR_XYZ"


def test_resolve_env_empty_string_returns_empty_string():
    conn = AdoConnection(base_url="http://x")
    assert conn._resolve_env("") == ""


# ── verify_ssl / SSL config ───────────────────────────────────────────────────

def test_from_config_default_verify_ssl_is_true():
    conn = AdoConnection.from_config({})
    assert conn.verify_ssl is True


def test_from_config_verify_ssl_true_string():
    conn = AdoConnection.from_config({"VerifySSL": "true"})
    assert conn.verify_ssl is True


def test_from_config_verify_ssl_false_string():
    conn = AdoConnection.from_config({"VerifySSL": "false"})
    assert conn.verify_ssl is False


def test_from_config_verify_ssl_false_case_insensitive():
    conn = AdoConnection.from_config({"VerifySSL": "False"})
    assert conn.verify_ssl is False


def test_from_config_verify_ssl_ca_bundle_path():
    conn = AdoConnection.from_config({"VerifySSL": "/etc/ssl/corp-ca.crt"})
    assert conn.verify_ssl == "/etc/ssl/corp-ca.crt"


def test_make_session_verify_true_by_default():
    conn = AdoConnection(base_url="http://x")
    session = conn._make_session()
    assert session.verify is True


def test_make_session_verify_false_when_configured():
    conn = AdoConnection(base_url="http://x", verify_ssl=False)
    session = conn._make_session()
    assert session.verify is False


def test_make_session_verify_ca_bundle_path():
    conn = AdoConnection(base_url="http://x", verify_ssl="/etc/ssl/corp-ca.crt")
    session = conn._make_session()
    assert session.verify == "/etc/ssl/corp-ca.crt"
