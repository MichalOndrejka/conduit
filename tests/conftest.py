import pytest
from app.models import SourceDefinition, SourceTypes, ConfigKeys


@pytest.fixture
def manual_source() -> SourceDefinition:
    return SourceDefinition(
        type=SourceTypes.WORK_ITEM_QUERY,
        name="My Doc",
        config={
            ConfigKeys.PROVIDER: "manual",
            ConfigKeys.CONTENT: "Hello world",
            ConfigKeys.TITLE: "Test Title",
        },
    )


@pytest.fixture
def custom_api_source() -> SourceDefinition:
    return SourceDefinition(
        type=SourceTypes.WORK_ITEM_QUERY,
        name="My API",
        config={
            ConfigKeys.PROVIDER: "custom",
            ConfigKeys.URL: "https://api.example.com/items",
            ConfigKeys.HTTP_METHOD: "GET",
            ConfigKeys.ITEMS_PATH: "data.items",
            ConfigKeys.TITLE_FIELD: "name",
            ConfigKeys.CONTENT_FIELDS: "description,status",
        },
    )
