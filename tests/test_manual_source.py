from unittest.mock import MagicMock

from app.models import ConfigKeys, SourceDefinition, SourceTypes
from app.sources.manual import ManualDocumentSource


def _source(content="body text", title="My Title", name="Source Name") -> SourceDefinition:
    cfg: dict[str, str] = {ConfigKeys.PROVIDER: "manual", ConfigKeys.CONTENT: content}
    if title:
        cfg[ConfigKeys.TITLE] = title
    return SourceDefinition(type=SourceTypes.WORK_ITEM_QUERY, name=name, config=cfg)


async def test_returns_single_document():
    docs = await ManualDocumentSource(_source()).fetch_documents()
    assert len(docs) == 1


async def test_document_id_equals_source_id():
    source = _source()
    docs = await ManualDocumentSource(source).fetch_documents()
    assert docs[0].id == source.id


async def test_document_text_is_content():
    docs = await ManualDocumentSource(_source(content="Important text")).fetch_documents()
    assert docs[0].text == "Important text"


async def test_document_title_from_config():
    docs = await ManualDocumentSource(_source(title="Config Title")).fetch_documents()
    assert docs[0].properties["title"] == "Config Title"


async def test_title_falls_back_to_source_name_when_absent():
    source = SourceDefinition(
        type=SourceTypes.WORK_ITEM_QUERY,
        name="Fallback Name",
        config={ConfigKeys.PROVIDER: "manual", ConfigKeys.CONTENT: "text"},
    )
    docs = await ManualDocumentSource(source).fetch_documents()
    assert docs[0].properties["title"] == "Fallback Name"


async def test_progress_callback_called_once_with_fetching_phase():
    cb = MagicMock()
    await ManualDocumentSource(_source()).fetch_documents(progress_cb=cb)
    cb.assert_called_once()
    assert cb.call_args[0][0].phase == "fetching"


async def test_progress_callback_message_includes_title():
    cb = MagicMock()
    await ManualDocumentSource(_source(title="Special Doc")).fetch_documents(progress_cb=cb)
    assert "Special Doc" in cb.call_args[0][0].message


async def test_none_progress_callback_does_not_raise():
    docs = await ManualDocumentSource(_source()).fetch_documents(progress_cb=None)
    assert len(docs) == 1


async def test_document_placeholder_returns_empty_list():
    from app.models import DOCUMENT_PLACEHOLDER
    source = SourceDefinition(
        type=SourceTypes.WORK_ITEM_QUERY,
        name="Imported Source",
        config={ConfigKeys.PROVIDER: "manual", ConfigKeys.CONTENT: DOCUMENT_PLACEHOLDER},
    )
    docs = await ManualDocumentSource(source).fetch_documents()
    assert docs == []


async def test_empty_content_returns_doc_with_empty_text():
    source = SourceDefinition(
        type=SourceTypes.WORK_ITEM_QUERY,
        name="Empty Source",
        config={ConfigKeys.PROVIDER: "manual"},
    )
    docs = await ManualDocumentSource(source).fetch_documents()
    assert len(docs) == 1
    assert docs[0].text == ""


async def test_tags_contain_source_id_and_name():
    source = _source(name="My Source")
    docs = await ManualDocumentSource(source).fetch_documents()
    assert docs[0].tags["source_id"] == source.id
    assert docs[0].tags["source_name"] == "My Source"
