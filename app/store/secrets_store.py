from __future__ import annotations

import asyncio
import json
import os
from pathlib import Path

from app.models import CredentialInfo


class SecretsStore:
    """Encrypted credential store backed by a JSON file.

    The credential name is the unique identifier — used as the storage key,
    the value stored in source configs, and the resolution key at sync time.

    Values are encrypted with Fernet (AES-128-CBC + HMAC-SHA256).
    Decrypted values are cached in memory for synchronous access from threads.
    """

    _KEY_FILE = ".secret_key"
    _STORE_FILE = "credentials.enc.json"

    def __init__(self, data_dir: Path) -> None:
        self._dir = data_dir
        self._store_path = data_dir / self._STORE_FILE
        self._lock = asyncio.Lock()
        self._fernet = self._load_fernet()
        # {name: {"note": str, "value": str (plaintext)}}
        self._cache: dict[str, dict] = {}
        self._load()

    # ── Key management ──────────────────────────────────────────────────────────

    def _load_fernet(self):
        from cryptography.fernet import Fernet
        env_key = os.environ.get("CONDUIT_SECRET_KEY", "").strip()
        if env_key:
            return Fernet(env_key.encode())
        key_file = self._dir / self._KEY_FILE
        if key_file.exists():
            key = key_file.read_bytes().strip()
        else:
            key = Fernet.generate_key()
            self._dir.mkdir(parents=True, exist_ok=True)
            key_file.write_bytes(key)
            try:
                key_file.chmod(0o600)
            except NotImplementedError:
                pass  # Windows doesn't support chmod; ACLs are set by OS defaults
        return Fernet(key)

    # ── Persistence ─────────────────────────────────────────────────────────────

    def _load(self) -> None:
        if not self._store_path.exists():
            return
        raw = json.loads(self._store_path.read_text(encoding="utf-8"))
        for name, entry in raw.items():
            try:
                plaintext = self._fernet.decrypt(entry["value"].encode()).decode()
                self._cache[name] = {
                    "note": entry.get("note", ""),
                    "value": plaintext,
                }
            except Exception:
                pass  # skip entries that can't be decrypted (wrong key / corrupt)

    def _save(self) -> None:
        out: dict[str, dict] = {}
        for name, entry in self._cache.items():
            encrypted = self._fernet.encrypt(entry["value"].encode()).decode()
            out[name] = {
                "note": entry["note"],
                "value": encrypted,
            }
        self._dir.mkdir(parents=True, exist_ok=True)
        tmp = self._store_path.with_suffix(".tmp")
        tmp.write_text(json.dumps(out, indent=2), encoding="utf-8")
        tmp.replace(self._store_path)

    # ── Public sync accessor (called from threads via asyncio.to_thread) ────────

    def get_value_sync(self, cred_name: str) -> str:
        entry = self._cache.get(cred_name)
        return entry["value"] if entry else ""

    # ── Async CRUD ──────────────────────────────────────────────────────────────

    async def list_all(self) -> list[CredentialInfo]:
        async with self._lock:
            return [
                CredentialInfo(id=name, name=name, note=e["note"])
                for name, e in self._cache.items()
            ]

    @staticmethod
    def _validate_name(name: str) -> None:
        if not name:
            raise ValueError("Credential name cannot be empty.")
        if "/" in name:
            raise ValueError("Credential name cannot contain '/'.")

    async def create(self, name: str, note: str, value: str) -> None:
        name = name.strip()
        self._validate_name(name)
        async with self._lock:
            if name in self._cache:
                raise ValueError(f"A credential named '{name}' already exists.")
            self._cache[name] = {"note": note.strip(), "value": value}
            self._save()

    async def update(
        self,
        old_name: str,
        new_name: str,
        note: str,
        value: str | None,
    ) -> str:
        """Update and return old_name so callers can cascade renames to sources.
        Returns "" if old_name does not exist."""
        new_name = new_name.strip()
        self._validate_name(new_name)
        async with self._lock:
            if old_name not in self._cache:
                return ""
            if new_name != old_name and new_name in self._cache:
                raise ValueError(f"A credential named '{new_name}' already exists.")
            entry = self._cache.pop(old_name)
            entry["note"] = note.strip()
            if value:
                entry["value"] = value
            self._cache[new_name] = entry
            self._save()
        return old_name

    async def delete(self, cred_name: str) -> None:
        async with self._lock:
            self._cache.pop(cred_name, None)
            self._save()

    async def has(self, cred_name: str) -> bool:
        async with self._lock:
            return cred_name in self._cache

    def sources_using(self, cred_name: str, sources: list) -> list[str]:
        if cred_name not in self._cache:
            return []
        result = []
        secret_fields = {"Pat", "Token", "Password", "ApiKeyValue"}
        for source in sources:
            for field in secret_fields:
                if source.config.get(field) == cred_name:
                    result.append(source.name)
                    break
        return result
