import base64

import pytest
from starlette.applications import Starlette
from starlette.responses import PlainTextResponse
from starlette.routing import Mount, Route
from starlette.testclient import TestClient

from app.web.auth import ApiKeyAuthMiddleware


async def _ok(request):
    return PlainTextResponse("ok")


def _client() -> TestClient:
    app = Starlette(routes=[Route("/", _ok)])
    app.add_middleware(ApiKeyAuthMiddleware)
    return TestClient(app)


def test_mounted_sub_app_is_protected(monkeypatch):
    """Middleware on the parent app must also gate routes served by a
    mounted sub-app, mirroring app.mount("/", mcp.streamable_http_app())."""
    monkeypatch.setenv("CONDUIT_API_KEY", "secret")
    sub_app = Starlette(routes=[Route("/mcp", _ok)])
    app = Starlette(routes=[Mount("/", app=sub_app)])
    app.add_middleware(ApiKeyAuthMiddleware)
    client = TestClient(app)

    assert client.get("/mcp").status_code == 401
    assert client.get("/mcp", headers={"Authorization": "Bearer secret"}).status_code == 200


def test_no_api_key_configured_allows_access(monkeypatch):
    monkeypatch.delenv("CONDUIT_API_KEY", raising=False)
    resp = _client().get("/")
    assert resp.status_code == 200


def test_missing_credentials_rejected(monkeypatch):
    monkeypatch.setenv("CONDUIT_API_KEY", "secret")
    resp = _client().get("/")
    assert resp.status_code == 401
    assert resp.headers["www-authenticate"] == 'Basic realm="Conduit"'


def test_wrong_bearer_token_rejected(monkeypatch):
    monkeypatch.setenv("CONDUIT_API_KEY", "secret")
    resp = _client().get("/", headers={"Authorization": "Bearer wrong"})
    assert resp.status_code == 401


def test_correct_bearer_token_allowed(monkeypatch):
    monkeypatch.setenv("CONDUIT_API_KEY", "secret")
    resp = _client().get("/", headers={"Authorization": "Bearer secret"})
    assert resp.status_code == 200


def test_correct_basic_auth_allowed(monkeypatch):
    monkeypatch.setenv("CONDUIT_API_KEY", "secret")
    creds = base64.b64encode(b"anyuser:secret").decode()
    resp = _client().get("/", headers={"Authorization": f"Basic {creds}"})
    assert resp.status_code == 200


def test_wrong_basic_auth_rejected(monkeypatch):
    monkeypatch.setenv("CONDUIT_API_KEY", "secret")
    creds = base64.b64encode(b"anyuser:wrong").decode()
    resp = _client().get("/", headers={"Authorization": f"Basic {creds}"})
    assert resp.status_code == 401
