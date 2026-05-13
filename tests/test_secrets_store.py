import json

import pytest
from cryptography.fernet import Fernet

from app.models import SourceDefinition, SourceTypes
from app.store.secrets_store import SecretsStore


@pytest.fixture
def store(tmp_path) -> SecretsStore:
    return SecretsStore(tmp_path)


# ── Key management ────────────────────────────────────────────────────────────

def test_auto_generates_key_file(tmp_path):
    SecretsStore(tmp_path)
    assert (tmp_path / ".secret_key").exists()


def test_auto_generated_key_is_valid_fernet_key(tmp_path):
    SecretsStore(tmp_path)
    key = (tmp_path / ".secret_key").read_bytes().strip()
    Fernet(key)  # raises if invalid


def test_reuses_existing_key_file(tmp_path):
    key = Fernet.generate_key()
    (tmp_path / ".secret_key").write_bytes(key)
    SecretsStore(tmp_path)  # must not regenerate a new key


def test_env_var_key_takes_precedence_over_file(tmp_path, monkeypatch):
    file_key = Fernet.generate_key()
    env_key = Fernet.generate_key()
    (tmp_path / ".secret_key").write_bytes(file_key)
    monkeypatch.setenv("CONDUIT_SECRET_KEY", env_key.decode())
    store = SecretsStore(tmp_path)
    token = store._fernet.encrypt(b"hello").decode()
    assert Fernet(env_key).decrypt(token.encode()) == b"hello"


def test_env_var_key_does_not_create_key_file(tmp_path, monkeypatch):
    monkeypatch.setenv("CONDUIT_SECRET_KEY", Fernet.generate_key().decode())
    SecretsStore(tmp_path)
    assert not (tmp_path / ".secret_key").exists()


# ── Encryption round-trip (persistence) ───────────────────────────────────────

async def test_create_persists_encrypted_value(tmp_path, store):
    await store.create("My PAT", "", "super-secret")
    raw = json.loads((tmp_path / "credentials.enc.json").read_text())
    stored_value = raw["My PAT"]["value"]
    assert stored_value != "super-secret"        # not plaintext on disk
    assert stored_value.startswith("g")          # Fernet tokens are base64url


async def test_name_is_storage_key(tmp_path, store):
    await store.create("MY_ADO_PAT", "", "token")
    raw = json.loads((tmp_path / "credentials.enc.json").read_text())
    assert "MY_ADO_PAT" in raw                   # name is the dict key, no UUID wrapper


async def test_value_not_stored_as_plaintext(tmp_path, store):
    await store.create("Key", "", "my-token-abc123")
    content = (tmp_path / "credentials.enc.json").read_text()
    assert "my-token-abc123" not in content


async def test_reload_decrypts_correctly(tmp_path):
    store1 = SecretsStore(tmp_path)
    await store1.create("Reloaded", "note", "reload-value")

    store2 = SecretsStore(tmp_path)
    assert store2.get_value_sync("Reloaded") == "reload-value"


async def test_wrong_key_silently_skips_entry(tmp_path):
    store1 = SecretsStore(tmp_path)
    await store1.create("Cred", "", "value")

    new_key = Fernet.generate_key()
    (tmp_path / ".secret_key").write_bytes(new_key)

    store2 = SecretsStore(tmp_path)
    assert await store2.list_all() == []   # entry silently skipped, no crash


async def test_atomic_write_uses_tmp_file_then_replaces(tmp_path, store, monkeypatch):
    seen_tmp = []
    original_replace = type(tmp_path / "x").replace

    def spy_replace(self, target):
        seen_tmp.append(self.name)
        return original_replace(self, target)

    monkeypatch.setattr(type(tmp_path / "x"), "replace", spy_replace)
    await store.create("A", "", "v")
    assert any(".tmp" in p for p in seen_tmp)


# ── CRUD ──────────────────────────────────────────────────────────────────────

async def test_get_value_sync_returns_value_by_name(store):
    await store.create("My PAT", "", "token-xyz")
    assert store.get_value_sync("My PAT") == "token-xyz"


async def test_get_value_sync_returns_empty_for_unknown_name(store):
    assert store.get_value_sync("no-such-name") == ""


async def test_list_all_empty_on_new_store(store):
    assert await store.list_all() == []


async def test_list_all_returns_credential_info(store):
    await store.create("My PAT", "expires soon", "value")
    creds = await store.list_all()
    assert len(creds) == 1
    assert creds[0].name == "My PAT"
    assert creds[0].id == "My PAT"    # id IS the name
    assert creds[0].note == "expires soon"


async def test_list_all_does_not_expose_value(store):
    await store.create("Key", "", "secret-value")
    creds = await store.list_all()
    assert not hasattr(creds[0], "value")


async def test_update_name_and_note(store):
    await store.create("Old Name", "old note", "token")
    await store.update("Old Name", "New Name", "new note", None)
    creds = await store.list_all()
    assert creds[0].name == "New Name"
    assert creds[0].note == "new note"
    assert store.get_value_sync("New Name") == "token"
    assert store.get_value_sync("Old Name") == ""    # old name no longer resolves


async def test_update_value(store):
    await store.create("Cred", "", "old-token")
    await store.update("Cred", "Cred", "", "new-token")
    assert store.get_value_sync("Cred") == "new-token"


async def test_update_none_value_preserves_existing(store):
    await store.create("Cred", "", "keep-me")
    await store.update("Cred", "Cred", "", None)
    assert store.get_value_sync("Cred") == "keep-me"


async def test_update_unknown_name_is_noop(store):
    old = await store.update("ghost", "X", "", "v")
    assert old == ""


async def test_update_returns_old_name(store):
    await store.create("Before", "", "v")
    old = await store.update("Before", "After", "", None)
    assert old == "Before"


async def test_update_returns_same_name_when_not_renamed(store):
    await store.create("Same", "", "v")
    old = await store.update("Same", "Same", "new note", None)
    assert old == "Same"


async def test_update_persisted_across_reload(tmp_path):
    store1 = SecretsStore(tmp_path)
    await store1.create("Cred", "", "original")
    await store1.update("Cred", "Renamed", "n", "updated")

    store2 = SecretsStore(tmp_path)
    assert store2.get_value_sync("Renamed") == "updated"
    assert store2.get_value_sync("Cred") == ""
    creds = await store2.list_all()
    assert creds[0].name == "Renamed"


async def test_delete_removes_entry(store):
    await store.create("To Delete", "", "val")
    await store.delete("To Delete")
    assert store.get_value_sync("To Delete") == ""
    assert await store.list_all() == []


async def test_delete_unknown_name_is_noop(store):
    await store.delete("ghost")  # must not raise


async def test_delete_persisted_across_reload(tmp_path):
    store1 = SecretsStore(tmp_path)
    await store1.create("Gone", "", "val")
    await store1.delete("Gone")

    store2 = SecretsStore(tmp_path)
    assert store2.get_value_sync("Gone") == ""


async def test_has_returns_true_after_create(store):
    await store.create("X", "", "v")
    assert await store.has("X") is True


async def test_has_returns_false_for_unknown(store):
    assert await store.has("ghost") is False


async def test_has_returns_false_after_delete(store):
    await store.create("X", "", "v")
    await store.delete("X")
    assert await store.has("X") is False


async def test_create_strips_whitespace_from_name(store):
    await store.create("  Padded  ", "  note  ", "val")
    creds = await store.list_all()
    assert creds[0].name == "Padded"
    assert creds[0].note == "note"


async def test_create_raises_on_duplicate_name(store):
    await store.create("MY_PAT", "", "v1")
    with pytest.raises(ValueError, match="already exists"):
        await store.create("MY_PAT", "", "v2")


async def test_create_allows_different_names(store):
    await store.create("PAT_A", "", "v1")
    await store.create("PAT_B", "", "v2")
    assert len(await store.list_all()) == 2


async def test_create_raises_on_slash_in_name(store):
    with pytest.raises(ValueError, match="cannot contain"):
        await store.create("my/pat", "", "v")


async def test_create_raises_on_empty_name(store):
    with pytest.raises(ValueError, match="cannot be empty"):
        await store.create("   ", "", "v")   # whitespace-only collapses to ""


async def test_update_raises_on_duplicate_name(store):
    await store.create("Alpha", "", "v1")
    await store.create("Beta", "", "v2")
    with pytest.raises(ValueError, match="already exists"):
        await store.update("Beta", "Alpha", "", None)


async def test_update_raises_on_slash_in_new_name(store):
    await store.create("Clean", "", "v")
    with pytest.raises(ValueError, match="cannot contain"):
        await store.update("Clean", "my/pat", "", None)


async def test_update_same_name_does_not_raise(store):
    await store.create("Same", "", "v")
    await store.update("Same", "Same", "updated note", None)  # must not raise


# ── sources_using ─────────────────────────────────────────────────────────────

def _source(name: str, **config) -> SourceDefinition:
    return SourceDefinition(type=SourceTypes.WORK_ITEM_QUERY, name=name, config=config)


async def test_sources_using_finds_pat(store):
    await store.create("MY_PAT", "", "v")
    sources = [_source("S1", Pat="MY_PAT"), _source("S2", Pat="OTHER_PAT")]
    assert store.sources_using("MY_PAT", sources) == ["S1"]


async def test_sources_using_finds_all_secret_fields(store):
    await store.create("MY_KEY", "", "v")
    sources = [
        _source("A", Pat="MY_KEY"),
        _source("B", Token="MY_KEY"),
        _source("C", Password="MY_KEY"),
        _source("D", ApiKeyValue="MY_KEY"),
    ]
    assert store.sources_using("MY_KEY", sources) == ["A", "B", "C", "D"]


async def test_sources_using_counts_source_once_even_if_multiple_fields_match(store):
    await store.create("K", "", "v")
    sources = [_source("Multi", Pat="K", Token="K")]
    assert store.sources_using("K", sources) == ["Multi"]


async def test_sources_using_returns_empty_for_unknown_name(store):
    sources = [_source("Other", Pat="SOME_CRED")]
    assert store.sources_using("ghost", sources) == []


async def test_sources_using_returns_empty_when_not_referenced(store):
    await store.create("Unused", "", "v")
    sources = [_source("Other", Pat="different-cred")]
    assert store.sources_using("Unused", sources) == []
