import pytest

from app.models import ConfigKeys, SourceDefinition, SourceTypes
from app.sources.custom_api import CustomApiSource

SAMPLE_RESPONSE = [
    {"title": "Item One", "description": "Desc 1", "status": "open"},
    {"title": "Item Two", "description": "Desc 2", "status": "closed"},
]


def _source(**overrides) -> SourceDefinition:
    cfg = {
        ConfigKeys.PROVIDER: "custom",
        ConfigKeys.URL: "https://api.example.com/data",
        ConfigKeys.HTTP_METHOD: "GET",
        ConfigKeys.ITEMS_PATH: "",
        ConfigKeys.TITLE_FIELD: "title",
        ConfigKeys.CONTENT_FIELDS: "",
    }
    cfg.update(overrides)
    return SourceDefinition(type=SourceTypes.WORK_ITEM_QUERY, name="Test API", config=cfg)


# ── _navigate ─────────────────────────────────────────────────────────────────

def test_navigate_empty_path_returns_data():
    data = {"a": 1}
    assert CustomApiSource._navigate(data, "") is data

def test_navigate_single_key():
    assert CustomApiSource._navigate({"items": [1, 2]}, "items") == [1, 2]

def test_navigate_nested_dot_path():
    data = {"data": {"records": [3, 4]}}
    assert CustomApiSource._navigate(data, "data.records") == [3, 4]

def test_navigate_missing_key_returns_empty_list():
    assert CustomApiSource._navigate({"a": 1}, "b") == []

def test_navigate_non_dict_mid_path_returns_empty_list():
    assert CustomApiSource._navigate({"a": "string"}, "a.b") == []

def test_navigate_none_value_returns_empty_list():
    assert CustomApiSource._navigate({"key": None}, "key") == []


# ── _field ────────────────────────────────────────────────────────────────────

def test_field_returns_string_value():
    assert CustomApiSource._field({"title": "Hello"}, "title") == "Hello"

def test_field_missing_key_returns_empty_string():
    assert CustomApiSource._field({"a": 1}, "missing") == ""

def test_field_none_value_returns_empty_string():
    assert CustomApiSource._field({"title": None}, "title") == ""

def test_field_non_dict_returns_empty_string():
    assert CustomApiSource._field("not a dict", "title") == ""

def test_field_numeric_coerced_to_str():
    assert CustomApiSource._field({"count": 42}, "count") == "42"


# ── fetch_documents (httpx_mock) ───────────────────────────────────────────────

async def test_returns_correct_document_count(httpx_mock):
    httpx_mock.add_response(json=SAMPLE_RESPONSE)
    docs = await CustomApiSource(_source()).fetch_documents()
    assert len(docs) == 2


async def test_document_ids_use_capi_pattern(httpx_mock):
    httpx_mock.add_response(json=SAMPLE_RESPONSE)
    source = _source()
    docs = await CustomApiSource(source).fetch_documents()
    assert docs[0].id == f"{source.id}_capi_0"
    assert docs[1].id == f"{source.id}_capi_1"


async def test_document_title_in_properties(httpx_mock):
    httpx_mock.add_response(json=SAMPLE_RESPONSE)
    docs = await CustomApiSource(_source()).fetch_documents()
    assert docs[0].properties["title"] == "Item One"


async def test_document_text_starts_with_title(httpx_mock):
    httpx_mock.add_response(json=SAMPLE_RESPONSE)
    docs = await CustomApiSource(_source()).fetch_documents()
    assert docs[0].text.startswith("Item One")


async def test_content_fields_filter_included_fields(httpx_mock):
    httpx_mock.add_response(json=SAMPLE_RESPONSE)
    docs = await CustomApiSource(_source(**{ConfigKeys.CONTENT_FIELDS: "description"})).fetch_documents()
    assert "description: Desc 1" in docs[0].text
    assert "status" not in docs[0].text


async def test_items_path_dot_traversal(httpx_mock):
    httpx_mock.add_response(json={"data": {"records": SAMPLE_RESPONSE}})
    docs = await CustomApiSource(_source(**{ConfigKeys.ITEMS_PATH: "data.records"})).fetch_documents()
    assert len(docs) == 2


async def test_single_dict_response_wrapped_as_list(httpx_mock):
    httpx_mock.add_response(json={"title": "Single", "body": "Only"})
    docs = await CustomApiSource(_source()).fetch_documents()
    assert len(docs) == 1


async def test_post_method_used_when_configured(httpx_mock):
    httpx_mock.add_response(json=SAMPLE_RESPONSE, method="POST")
    docs = await CustomApiSource(_source(**{ConfigKeys.HTTP_METHOD: "POST"})).fetch_documents()
    assert len(docs) == 2


async def test_missing_url_raises_value_error():
    source = SourceDefinition(
        type=SourceTypes.WORK_ITEM_QUERY, name="Bad",
        config={ConfigKeys.PROVIDER: "custom"},
    )
    with pytest.raises(ValueError, match="URL"):
        await CustomApiSource(source).fetch_documents()


async def test_tags_contain_source_metadata(httpx_mock):
    httpx_mock.add_response(json=SAMPLE_RESPONSE)
    source = _source()
    docs = await CustomApiSource(source).fetch_documents()
    assert docs[0].tags["source_id"] == source.id
    assert docs[0].tags["source_name"] == "Test API"


def _mock_store(monkeypatch, values: dict):
    from app import container as _container
    from unittest.mock import MagicMock
    mock = MagicMock()
    mock.get_value_sync.side_effect = lambda cred_id: values.get(cred_id, "")
    monkeypatch.setattr(_container, "secrets_store", mock, raising=False)
    return mock


async def test_bearer_auth_header_sent(httpx_mock, monkeypatch):
    _mock_store(monkeypatch, {"cred-token-id": "secret-token"})
    httpx_mock.add_response(json=[])
    await CustomApiSource(_source(**{
        ConfigKeys.AUTH_TYPE: "bearer",
        ConfigKeys.TOKEN: "cred-token-id",
    })).fetch_documents()
    assert httpx_mock.get_requests()[0].headers["Authorization"] == "Bearer secret-token"


async def test_apikey_auth_header_sent(httpx_mock, monkeypatch):
    _mock_store(monkeypatch, {"cred-key-id": "abc123"})
    httpx_mock.add_response(json=[])
    await CustomApiSource(_source(**{
        ConfigKeys.AUTH_TYPE: "apikey",
        ConfigKeys.API_KEY_HEADER: "X-Custom-Key",
        ConfigKeys.API_KEY_VALUE: "cred-key-id",
    })).fetch_documents()
    assert httpx_mock.get_requests()[0].headers["X-Custom-Key"] == "abc123"


async def test_no_auth_sends_no_authorization_header(httpx_mock):
    httpx_mock.add_response(json=[])
    await CustomApiSource(_source(**{ConfigKeys.AUTH_TYPE: "none"})).fetch_documents()
    assert "authorization" not in httpx_mock.get_requests()[0].headers


async def test_absent_auth_type_sends_no_authorization_header(httpx_mock):
    """AUTH_TYPE key absent entirely should behave the same as 'none'."""
    httpx_mock.add_response(json=[])
    cfg = {k: v for k, v in _source().config.items() if k != ConfigKeys.AUTH_TYPE}
    source = SourceDefinition(type=SourceTypes.WORK_ITEM_QUERY, name="No Auth Key", config=cfg)
    await CustomApiSource(source).fetch_documents()
    assert "authorization" not in httpx_mock.get_requests()[0].headers
