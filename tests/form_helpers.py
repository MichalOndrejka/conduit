"""
Shared helpers for _build_source_from_form test suite.
Imported by all test_form_*.py modules.
"""
from app.models import ConfigKeys, SourceTypes
from app.web.routes import _build_source_from_form


class MockForm:
    """Minimal dict-like object that mimics Starlette's FormData."""

    def __init__(self, data: dict):
        self._data = data

    def get(self, key, default=None):
        return self._data.get(key, default)

    def getlist(self, key):
        val = self._data.get(key)
        if val is None:
            return []
        return val if isinstance(val, list) else [val]


def _form(**fields) -> MockForm:
    return MockForm(fields)


def _cfg(source_type: str, **fields):
    """Build a source from a form and return its config dict."""
    form = _form(source_type=source_type, name="Test Source", **fields)
    return _build_source_from_form(form).config
