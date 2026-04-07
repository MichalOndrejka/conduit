from __future__ import annotations

import re
from app.models import CodeUnit, CodeUnitKind
from app.parsing.base import CodeParser

_HEADING_RE = re.compile(r"^(#{1,3})\s+(.+)$", re.MULTILINE)


class MarkdownParser(CodeParser):
    def can_parse(self, extension: str) -> bool:
        return extension.lower() in (".md", ".markdown")

    def parse(self, content: str, file_path: str) -> list[CodeUnit]:
        try:
            return self._parse(content, file_path)
        except Exception:
            return []

    def _parse(self, content: str, file_path: str) -> list[CodeUnit]:
        headings = list(_HEADING_RE.finditer(content))
        if not headings:
            return [CodeUnit(
                kind=CodeUnitKind.FILE,
                name=file_path.split("/")[-1].split("\\")[-1],
                full_text=content[:4000],
                language="Markdown",
                file_path=file_path,
            )]

        units: list[CodeUnit] = []
        for i, m in enumerate(headings):
            start = m.start()
            end = headings[i + 1].start() if i + 1 < len(headings) else len(content)
            section_text = content[start:end].strip()
            name = m.group(2).strip()

            units.append(CodeUnit(
                kind=CodeUnitKind.SECTION,
                name=name,
                full_text=section_text[:4000],
                language="Markdown",
                file_path=file_path,
            ))

        return units
