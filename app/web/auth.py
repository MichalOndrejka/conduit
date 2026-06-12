from __future__ import annotations

import base64
import hmac
import os


class ApiKeyAuthMiddleware:
    """Require a shared secret on every request when CONDUIT_API_KEY is set.

    Accepts `Authorization: Bearer <key>` (for MCP/API clients) or HTTP Basic
    auth with the key as the password (so browsers show a native login
    prompt). A no-op if CONDUIT_API_KEY is unset, preserving the existing
    open-by-default behavior for local/dev use.
    """

    def __init__(self, app):
        self._app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            await self._app(scope, receive, send)
            return

        api_key = os.environ.get("CONDUIT_API_KEY", "")
        if not api_key or _is_authorized(scope, api_key):
            await self._app(scope, receive, send)
            return

        await _send_unauthorized(send)


def _is_authorized(scope, api_key: str) -> bool:
    headers = dict(scope.get("headers") or [])
    auth = headers.get(b"authorization", b"").decode("latin-1")
    if not auth:
        return False
    scheme, _, value = auth.partition(" ")
    value = value.strip()
    scheme = scheme.lower()
    if scheme == "bearer":
        return hmac.compare_digest(value, api_key)
    if scheme == "basic":
        try:
            decoded = base64.b64decode(value).decode("utf-8")
        except Exception:
            return False
        _, _, password = decoded.partition(":")
        return hmac.compare_digest(password, api_key)
    return False


async def _send_unauthorized(send) -> None:
    body = b'{"error":"Unauthorized"}'
    await send({
        "type": "http.response.start",
        "status": 401,
        "headers": [
            (b"content-type", b"application/json"),
            (b"www-authenticate", b'Basic realm="Conduit"'),
            (b"content-length", str(len(body)).encode()),
        ],
    })
    await send({"type": "http.response.body", "body": body})
