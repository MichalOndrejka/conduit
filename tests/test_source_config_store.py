import json

import pytest

from app.models import SourceDefinition, SourceTypes
from app.store.source_config import SourceConfigStore, _normalise_keys


@pytest.fixture
def store(tmp_path) -> SourceConfigStore:
    return SourceConfigStore(str(tmp_path / "sources.json"))


@pytest.fixture
def sample_source() -> SourceDefinition:
    return SourceDefinition(
        type=SourceTypes.WORK_ITEM_QUERY,
        name="Test Source",
        config={"Pat": "secret-pat", "BaseUrl": "https://dev.azure.com/org"},
    )


# ── get_all ───────────────────────────────────────────────────────────────────

async def test_get_all_returns_empty_list_when_no_file(store):
    assert await store.get_all() == []


# ── save and retrieval ────────────────────────────────────────────────────────

async def test_save_and_get_all_returns_one_source(store, sample_source):
    await store.save(sample_source)
    sources = await store.get_all()
    assert len(sources) == 1
    assert sources[0].id == sample_source.id
    assert sources[0].name == "Test Source"


async def test_save_same_id_updates_in_place(store, sample_source):
    await store.save(sample_source)
    sample_source.name = "Updated Name"
    await store.save(sample_source)
    sources = await store.get_all()
    assert len(sources) == 1
    assert sources[0].name == "Updated Name"


async def test_get_by_id_returns_correct_source(store, sample_source):
    await store.save(sample_source)
    result = await store.get_by_id(sample_source.id)
    assert result is not None
    assert result.id == sample_source.id


async def test_get_by_id_returns_none_for_missing_id(store):
    assert await store.get_by_id("nonexistent") is None


# ── delete ────────────────────────────────────────────────────────────────────

async def test_delete_removes_source(store, sample_source):
    await store.save(sample_source)
    await store.delete(sample_source.id)
    assert await store.get_all() == []


async def test_delete_nonexistent_id_does_not_raise(store):
    await store.delete("does-not-exist")


# ── reset_all_sync_status ─────────────────────────────────────────────────────

async def test_reset_all_sync_status_updates_all_sources(store):
    s1 = SourceDefinition(type="t", name="A", sync_status="completed")
    s2 = SourceDefinition(type="t", name="B", sync_status="failed", sync_error="oops")
    await store.save(s1)
    await store.save(s2)
    await store.reset_all_sync_status("needs-reindex")
    sources = await store.get_all()
    assert all(s.sync_status == "needs-reindex" for s in sources)
    assert all(s.sync_error is None for s in sources)


# ── export_stripped ────────────────────────────────────────────────────────────

async def test_export_stripped_blanks_pat(store, sample_source):
    await store.save(sample_source)
    assert store.export_stripped()[0]["config"]["Pat"] == ""


async def test_export_stripped_blanks_token_preserves_base_url(store):
    src = SourceDefinition(type="t", name="n", config={"Token": "tok", "BaseUrl": "http://x"})
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert exported["Token"] == ""
    assert exported["BaseUrl"] == "http://x"


async def test_export_stripped_blanks_password_and_api_key_value(store):
    src = SourceDefinition(type="t", name="n", config={"Password": "pw", "ApiKeyValue": "k"})
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert exported["Password"] == ""
    assert exported["ApiKeyValue"] == ""


# ── _normalise_keys ────────────────────────────────────────────────────────────

def test_normalise_keys_converts_camel_case():
    raw = {"lastSyncedAt": "2024-01-01", "syncStatus": "completed", "syncError": None}
    result = _normalise_keys(raw)
    assert "last_synced_at" in result
    assert "sync_status" in result
    assert "sync_error" in result
    assert "lastSyncedAt" not in result


def test_normalise_keys_preserves_unknown_keys():
    raw = {"id": "abc", "type": "workitem-query", "name": "x"}
    assert _normalise_keys(raw) == raw


# ── resilience ────────────────────────────────────────────────────────────────

async def test_corrupt_json_file_returns_empty_list(tmp_path):
    path = tmp_path / "sources.json"
    path.write_text("{ this is not json }")
    assert await SourceConfigStore(str(path)).get_all() == []


async def test_legacy_camel_case_json_round_trip(tmp_path):
    path = tmp_path / "sources.json"
    path.write_text(json.dumps([{
        "id": "some-id",
        "type": "workitem-query",
        "name": "Legacy",
        "lastSyncedAt": None,
        "syncStatus": "completed",
        "syncError": None,
        "config": {},
    }]))
    sources = await SourceConfigStore(str(path)).get_all()
    assert len(sources) == 1
    assert sources[0].sync_status == "completed"
