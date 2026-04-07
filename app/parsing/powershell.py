from __future__ import annotations

import re
from app.models import CodeUnit, CodeUnitKind
from app.parsing.base import CodeParser

_FUNC_RE = re.compile(
    r"(?P<help><#[\s\S]*?#>\s*)?"
    r"function\s+(?P<name>[\w-]+)\s*(?:\([^)]*\))?\s*\{",
    re.IGNORECASE | re.MULTILINE,
)
_SYNOPSIS_RE = re.compile(r"\.SYNOPSIS\s*\n(.*?)(?:\n\.|$)", re.DOTALL | re.IGNORECASE)


def _extract_synopsis(help_block: str) -> str:
    if not help_block:
        return ""
    m = _SYNOPSIS_RE.search(help_block)
    return m.group(1).strip() if m else ""


def _extract_block(content: str, start: int, max_lines: int = 80) -> str:
    return "\n".join(content[start:].splitlines()[:max_lines])


class PowerShellParser(CodeParser):
    def can_parse(self, extension: str) -> bool:
        return extension.lower() in (".ps1", ".psm1", ".psd1")

    def parse(self, content: str, file_path: str) -> list[CodeUnit]:
        try:
            return self._parse(content, file_path)
        except Exception:
            return []

    def _parse(self, content: str, file_path: str) -> list[CodeUnit]:
        units: list[CodeUnit] = []

        for m in _FUNC_RE.finditer(content):
            name = m.group("name")
            help_block = m.group("help") or ""
            doc = _extract_synopsis(help_block)
            block = _extract_block(content, m.start())

            units.append(CodeUnit(
                kind=CodeUnitKind.FUNCTION,
                name=name,
                is_public=not name.startswith("_"),
                doc_comment=doc or None,
                full_text=block[:3000],
                language="PowerShell",
                file_path=file_path,
            ))

        if not units:
            units.append(CodeUnit(
                kind=CodeUnitKind.FILE,
                name=file_path.split("/")[-1].split("\\")[-1],
                full_text=content[:4000],
                language="PowerShell",
                file_path=file_path,
            ))

        return units
