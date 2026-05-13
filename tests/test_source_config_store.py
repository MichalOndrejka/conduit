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
# Credential fields (Pat/Token/Password/ApiKeyValue) store the credential NAME,
# which is the portable cross-system identifier. export_stripped must preserve
# them so importers can create a credential with the same name and skip
# reconfiguring every source.

async def test_export_stripped_preserves_pat_name(store, sample_source):
    await store.save(sample_source)
    assert store.export_stripped()[0]["config"]["Pat"] == "secret-pat"


async def test_export_stripped_preserves_token_and_base_url(store):
    src = SourceDefinition(type="t", name="n", config={"Token": "MY_TOKEN", "BaseUrl": "http://x"})
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert exported["Token"] == "MY_TOKEN"
    assert exported["BaseUrl"] == "http://x"


async def test_export_stripped_preserves_password_and_api_key_value(store):
    src = SourceDefinition(type="t", name="n", config={"Password": "MY_PW", "ApiKeyValue": "MY_KEY"})
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert exported["Password"] == "MY_PW"
    assert exported["ApiKeyValue"] == "MY_KEY"


async def test_export_stripped_includes_credential_fields_in_key_set(store):
    src = SourceDefinition(type="t", name="n", config={
        "Pat": "MY_PAT", "BaseUrl": "https://x", "AuthType": "pat", "ApiVersion": "7.1"
    })
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert set(exported.keys()) == {"Pat", "BaseUrl", "AuthType", "ApiVersion"}


async def test_export_stripped_replaces_manual_content_with_placeholder(store):
    from app.models import ConfigKeys, DOCUMENT_PLACEHOLDER
    src = SourceDefinition(type="documentation", name="Manual Doc", config={
        ConfigKeys.PROVIDER: "manual",
        ConfigKeys.MANUAL_TYPE: "text",
        ConfigKeys.CONTENT: "This is sensitive document content that should not be exported",
    })
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert exported[ConfigKeys.CONTENT] == DOCUMENT_PLACEHOLDER


async def test_export_stripped_replaces_upload_doc_type_content_with_placeholder(store):
    from app.models import ConfigKeys, DOCUMENT_PLACEHOLDER
    src = SourceDefinition(type="documentation", name="Uploaded Doc", config={
        ConfigKeys.DOC_TYPE: "upload",
        ConfigKeys.CONTENT: "Extracted PDF content",
        ConfigKeys.TITLE: "doc.pdf",
    })
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert exported[ConfigKeys.CONTENT] == DOCUMENT_PLACEHOLDER


async def test_export_stripped_preserves_content_for_non_manual_sources(store):
    from app.models import ConfigKeys
    src = SourceDefinition(type="documentation", name="Wiki", config={
        ConfigKeys.DOC_TYPE: "wiki",
        ConfigKeys.WIKI_NAME: "MyWiki",
    })
    await store.save(src)
    exported = store.export_stripped()[0]["config"]
    assert ConfigKeys.CONTENT not in exported


# ── rename_credential_references ─────────────────────────────────────────────

async def test_rename_credential_updates_all_matching_sources(store):
    src1 = SourceDefinition(type="t", name="S1", config={"Pat": "OLD_NAME", "BaseUrl": "http://x"})
    src2 = SourceDefinition(type="t", name="S2", config={"Token": "OLD_NAME"})
    src3 = SourceDefinition(type="t", name="S3", config={"Pat": "OTHER"})
    for s in [src1, src2, src3]:
        await store.save(s)

    await store.rename_credential_references("OLD_NAME", "NEW_NAME")

    sources = await store.get_all()
    by_name = {s.name: s for s in sources}
    assert by_name["S1"].config["Pat"] == "NEW_NAME"
    assert by_name["S2"].config["Token"] == "NEW_NAME"
    assert by_name["S3"].config["Pat"] == "OTHER"   # untouched


async def test_rename_credential_noop_when_not_referenced(store):
    src = SourceDefinition(type="t", name="S", config={"Pat": "KEEP"})
    await store.save(src)
    await store.rename_credential_references("GHOST", "NEW")  # must not raise or corrupt
    assert (await store.get_all())[0].config["Pat"] == "KEEP"


# ── _normalise_keys ────────────────────────────────────────────────────────────

def test_normalise_keys_converts_camel_case():
    raw = {"lastSyncedAt": "2024-01-01", "syncStatus": "completed", "syncError": None}
    result = _normalise_keys(raw)
    assert "last_synced_at" in result
    assert "sync_status" in result
    assert "sync_error" in result
    assert "lastSyncedAt" not in result


def test_normalise_keys_preserves_unknown_keys():
    raw = {"id": "abc", "type": "workitem", "name": "x"}
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
        "type": "workitem",
        "name": "Legacy",
        "lastSyncedAt": None,
        "syncStatus": "completed",
        "syncError": None,
        "config": {},
    }]))
    sources = await SourceConfigStore(str(path)).get_all()
    assert len(sources) == 1
    assert sources[0].sync_status == "completed"
