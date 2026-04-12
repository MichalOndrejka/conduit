from __future__ import annotations

import asyncio
import os
from dataclasses import dataclass
from typing import Any, Optional
from urllib.parse import urljoin

import requests
from requests.auth import HTTPBasicAuth


@dataclass
class AdoConnection:
    base_url: str                        # project-level URL, no trailing slash
    auth_type: str = "none"              # pat | bearer | ntlm | negotiate | apikey | none
    api_version: str = "7.1"
    pat: str = ""                        # env var name holding the PAT
    token: str = ""                      # env var name holding the bearer token
    username: str = ""
    password: str = ""                   # env var name holding the password
    domain: str = ""
    api_key_header: str = ""
    api_key_value: str = ""              # env var name holding the API key value
    verify_ssl: bool | str = True        # True | False | path to CA bundle

    @classmethod
    def from_config(cls, cfg: dict[str, str]) -> "AdoConnection":
        raw_verify = cfg.get("VerifySSL", "true").strip()
        if raw_verify.lower() == "false":
            verify_ssl: bool | str = False
        elif raw_verify.lower() in ("true", ""):
            verify_ssl = True
        else:
            verify_ssl = raw_verify   # treat as path to CA bundle

        return cls(
            base_url=cfg.get("BaseUrl", "").rstrip("/"),
            auth_type=cfg.get("AuthType", "none"),
            api_version=cfg.get("ApiVersion", "7.1"),
            pat=cfg.get("Pat", ""),
            token=cfg.get("Token", ""),
            username=cfg.get("Username", ""),
            password=cfg.get("Password", ""),
            domain=cfg.get("Domain", ""),
            api_key_header=cfg.get("ApiKeyHeader", ""),
            api_key_value=cfg.get("ApiKeyValue", ""),
            verify_ssl=verify_ssl,
        )

    def _resolve_env(self, var_name: str) -> str:
        if not var_name:
            return ""
        return os.environ.get(var_name, var_name)

    def _make_session(self) -> requests.Session:
        session = requests.Session()
        session.verify = self.verify_ssl
        auth_type = self.auth_type.lower()

        if auth_type == "pat":
            pat = self._resolve_env(self.pat)
            session.auth = HTTPBasicAuth("", pat)
        elif auth_type == "bearer":
            token = self._resolve_env(self.token)
            session.headers["Authorization"] = f"Bearer {token}"
        elif auth_type == "ntlm":
            from requests_ntlm import HttpNtlmAuth
            password = self._resolve_env(self.password)
            user = f"{self.domain}\\{self.username}" if self.domain else self.username
            session.auth = HttpNtlmAuth(user, password)
        elif auth_type == "negotiate":
            try:
                from requests_negotiate_sspi import HttpNegotiateAuth
                session.auth = HttpNegotiateAuth()
            except ImportError:
                from requests_ntlm import HttpNtlmAuth
                password = self._resolve_env(self.password)
                user = f"{self.domain}\\{self.username}" if self.domain else self.username
                session.auth = HttpNtlmAuth(user, password)
        elif auth_type == "apikey":
            api_key = self._resolve_env(self.api_key_value)
            session.headers[self.api_key_header] = api_key

        return session

    def _url(self, api_path: str, **params: Any) -> str:
        if not self.base_url:
            raise ValueError(
                "BaseUrl is not configured for this source. "
                "Edit the source and enter the full project URL (e.g. https://tfs.company.com/DefaultCollection/MyProject)."
            )
        url = f"{self.base_url}/{api_path.lstrip('/')}"
        query = dict(params)
        query["api-version"] = self.api_version
        query_str = "&".join(f"{k}={v}" for k, v in query.items())
        return f"{url}?{query_str}"

    def _get(self, api_path: str, **params: Any) -> Any:
        import json as _json
        session = self._make_session()
        url = self._url(api_path, **params)
        resp = session.get(url, timeout=60)
        resp.raise_for_status()
        _check_html_auth_redirect(resp, url, "GET")
        try:
            return resp.json()
        except _json.JSONDecodeError as exc:
            preview = resp.text[:300] if resp.text else "<empty body>"
            raise ValueError(
                f"ADO returned non-JSON (HTTP {resp.status_code}) for GET {url}\n"
                f"Body preview: {preview}"
            ) from exc

    def _post(self, api_path: str, body: Any, **params: Any) -> Any:
        import json as _json
        session = self._make_session()
        url = self._url(api_path, **params)
        resp = session.post(url, json=body, timeout=60)
        resp.raise_for_status()
        _check_html_auth_redirect(resp, url, "POST")
        try:
            return resp.json()
        except _json.JSONDecodeError as exc:
            preview = resp.text[:300] if resp.text else "<empty body>"
            raise ValueError(
                f"ADO returned non-JSON (HTTP {resp.status_code}) for POST {url}\n"
                f"Body preview: {preview}"
            ) from exc

    def _get_text(self, api_path: str, **params: Any) -> str:
        session = self._make_session()
        url = self._url(api_path, **params)
        resp = session.get(url, timeout=60)
        resp.raise_for_status()
        return resp.text


def _check_html_auth_redirect(resp, url: str, method: str) -> None:
    """Raise a clear error when ADO returns an HTML sign-in page (HTTP 203 or text/html)."""
    ct = resp.headers.get("Content-Type", "")
    is_html = "text/html" in ct or "xhtml" in ct
    if not is_html and resp.text:
        is_html = resp.text.lstrip().startswith("<!DOCTYPE") or resp.text.lstrip().startswith("<html")
    if is_html:
        raise ValueError(
            f"ADO returned an HTML sign-in page (HTTP {resp.status_code}) for {method} {url}. "
            "Authentication failed — check that your PAT environment variable is set and has the required scopes."
        )


class AdoClient:
    """Sync ADO REST client. All public methods are async via asyncio.to_thread."""

    def _run(self, fn, *args, **kwargs):
        return asyncio.to_thread(fn, *args, **kwargs)

    async def run_work_item_query(
        self, conn: AdoConnection, wiql: str, fields: list[str]
    ) -> list[dict]:
        return await asyncio.to_thread(self._sync_run_work_item_query, conn, wiql, fields)

    def _sync_run_work_item_query(
        self, conn: AdoConnection, wiql: str, fields: list[str]
    ) -> list[dict]:
        body = {"query": wiql}
        result = conn._post("_apis/wit/wiql", body)
        ids = [str(item["id"]) for item in result.get("workItems", [])]
        if not ids:
            return []

        all_items: list[dict] = []
        batch = 200
        for i in range(0, len(ids), batch):
            chunk = ids[i:i + batch]
            ids_param = ",".join(chunk)
            params: dict[str, Any] = {"ids": ids_param}
            if fields:
                params["fields"] = ",".join(fields)
            data = conn._get("_apis/wit/workitems", **params)
            all_items.extend(data.get("value", []))
        return all_items

    async def get_file_tree(
        self, conn: AdoConnection, repository: str, branch: str, scope_path: str = "/"
    ) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_file_tree, conn, repository, branch, scope_path)

    def _sync_get_file_tree(
        self, conn: AdoConnection, repository: str, branch: str, scope_path: str
    ) -> list[dict]:
        data = conn._get(
            f"_apis/git/repositories/{repository}/items",
            scopePath=scope_path,
            recursionLevel="Full",
            versionDescriptor_version=branch,
            versionDescriptor_versionType="branch",
        )
        return [i for i in data.get("value", []) if not i.get("isFolder", False)]

    async def get_file_content(
        self, conn: AdoConnection, repository: str, branch: str, path: str
    ) -> str:
        return await asyncio.to_thread(
            self._sync_get_file_content, conn, repository, branch, path
        )

    def _sync_get_file_content(
        self, conn: AdoConnection, repository: str, branch: str, path: str
    ) -> str:
        return conn._get_text(
            f"_apis/git/repositories/{repository}/items",
            path=path,
            versionDescriptor_version=branch,
            versionDescriptor_versionType="branch",
        )

    async def get_builds(
        self, conn: AdoConnection, pipeline_id: int, last_n: int = 5
    ) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_builds, conn, pipeline_id, last_n)

    def _sync_get_builds(
        self, conn: AdoConnection, pipeline_id: int, last_n: int
    ) -> list[dict]:
        data = conn._get(
            "_apis/build/builds",
            definitions=str(pipeline_id),
            **{"$top": str(last_n)},
        )
        return data.get("value", [])

    async def get_build_timeline(self, conn: AdoConnection, build_id: int) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_build_timeline, conn, build_id)

    def _sync_get_build_timeline(self, conn: AdoConnection, build_id: int) -> list[dict]:
        data = conn._get(f"_apis/build/builds/{build_id}/timeline")
        return data.get("records", [])

    async def get_wikis(self, conn: AdoConnection) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_wikis, conn)

    def _sync_get_wikis(self, conn: AdoConnection) -> list[dict]:
        data = conn._get("_apis/wiki/wikis")
        return data.get("value", [])

    async def get_wiki_items(
        self, conn: AdoConnection, wiki_id: str, path: str = "/"
    ) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_wiki_items, conn, wiki_id, path)

    def _sync_get_wiki_items(
        self, conn: AdoConnection, wiki_id: str, path: str
    ) -> list[dict]:
        try:
            data = conn._get(
                f"_apis/wiki/wikis/{wiki_id}/pages",
                path=path,
                recursionLevel="full",
                includeContent="true",
            )
            return [data] if data else []
        except Exception:
            return []

    async def get_wiki_page(
        self, conn: AdoConnection, wiki_id: str, path: str
    ) -> dict:
        return await asyncio.to_thread(self._sync_get_wiki_page, conn, wiki_id, path)

    def _sync_get_wiki_page(
        self, conn: AdoConnection, wiki_id: str, path: str
    ) -> dict:
        return conn._get(
            f"_apis/wiki/wikis/{wiki_id}/pages",
            path=path,
            includeContent="true",
        )

    async def get_pull_requests(
        self, conn: AdoConnection, repository: str, status: str = "all", top: int = 200
    ) -> list[dict]:
        return await asyncio.to_thread(
            self._sync_get_pull_requests, conn, repository, status, top
        )

    def _sync_get_pull_requests(
        self, conn: AdoConnection, repository: str, status: str, top: int
    ) -> list[dict]:
        data = conn._get(
            f"_apis/git/repositories/{repository}/pullrequests",
            **{"searchCriteria.status": status, "$top": str(top)},
        )
        return data.get("value", [])

    async def get_test_runs(self, conn: AdoConnection, top: int = 10) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_test_runs, conn, top)

    def _sync_get_test_runs(self, conn: AdoConnection, top: int) -> list[dict]:
        data = conn._get("_apis/test/runs", **{"$top": str(top)})
        return data.get("value", [])

    async def get_test_results(
        self, conn: AdoConnection, run_id: int, top: int = 200
    ) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_test_results, conn, run_id, top)

    def _sync_get_test_results(
        self, conn: AdoConnection, run_id: int, top: int
    ) -> list[dict]:
        data = conn._get(
            f"_apis/test/runs/{run_id}/results",
            **{"$top": str(top)},
        )
        return data.get("value", [])

    async def get_commits(
        self, conn: AdoConnection, repository: str, branch: str = "", top: int = 100
    ) -> list[dict]:
        return await asyncio.to_thread(self._sync_get_commits, conn, repository, branch, top)

    def _sync_get_commits(
        self, conn: AdoConnection, repository: str, branch: str, top: int
    ) -> list[dict]:
        params: dict[str, Any] = {"$top": str(top)}
        if branch:
            params["searchCriteria.itemVersion.version"] = branch
        data = conn._get(
            f"_apis/git/repositories/{repository}/commits",
            **params,
        )
        return data.get("value", [])
