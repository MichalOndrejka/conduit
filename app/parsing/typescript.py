from __future__ import annotations

import re
from app.models import CodeUnit, CodeUnitKind
from app.parsing.base import CodeParser

_JSDOC_RE = re.compile(r"(/\*\*[\s\S]*?\*/)\s*", re.MULTILINE)

_CLASS_RE = re.compile(
    r"(?:export\s+)?(?:abstract\s+)?class\s+(\w+)", re.MULTILINE
)
_INTERFACE_RE = re.compile(r"(?:export\s+)?interface\s+(\w+)", re.MULTILINE)
_ENUM_RE = re.compile(r"(?:export\s+)?enum\s+(\w+)", re.MULTILINE)
_TYPE_RE = re.compile(r"(?:export\s+)?type\s+(\w+)\s*=", re.MULTILINE)
_FUNC_RE = re.compile(
    r"(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\([^)]*\)", re.MULTILINE
)
_ARROW_RE = re.compile(
    r"(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\([^)]*\)\s*=>",
    re.MULTILINE,
)


def _preceding_jsdoc(content: str, match_start: int) -> str:
    before = content[:match_start]
    m = _JSDOC_RE.search(before + " ")
    if m and m.end() >= len(before) - 5:
        raw = m.group(1)
        lines = raw.splitlines()
        cleaned = []
        for line in lines:
            line = re.sub(r"^\s*\*+\s?", "", line).strip()
            if line and line not in ("/**", "*/"):
                cleaned.append(line)
        return " ".join(cleaned)
    return ""


def _extract_block(content: str, start: int, max_lines: int = 60) -> str:
    lines = content[start:].splitlines()[:max_lines]
    return "\n".join(lines)


class TypeScriptParser(CodeParser):
    def can_parse(self, extension: str) -> bool:
        return extension.lower() in (".ts", ".tsx", ".js", ".jsx")

    def parse(self, content: str, file_path: str) -> list[CodeUnit]:
        try:
            return self._parse(content, file_path)
        except Exception:
            return []

    def _parse(self, content: str, file_path: str) -> list[CodeUnit]:
        units: list[CodeUnit] = []

        for pattern, kind in [
            (_CLASS_RE, CodeUnitKind.CLASS),
            (_INTERFACE_RE, CodeUnitKind.INTERFACE),
            (_ENUM_RE, CodeUnitKind.ENUM),
            (_TYPE_RE, CodeUnitKind.TYPE),
            (_FUNC_RE, CodeUnitKind.FUNCTION),
            (_ARROW_RE, CodeUnitKind.FUNCTION),
        ]:
            for m in pattern.finditer(content):
                name = m.group(1)
                doc = _preceding_jsdoc(content, m.start())
                block = _extract_block(content, m.start())
                units.append(CodeUnit(
                    kind=kind,
                    name=name,
                    is_public="export" in content[max(0, m.start() - 20):m.start() + 30],
                    doc_comment=doc or None,
                    full_text=block[:3000],
                    language="TypeScript",
                    file_path=file_path,
                ))

        if not units:
            units.append(CodeUnit(
                kind=CodeUnitKind.FILE,
                name=file_path.split("/")[-1].split("\\")[-1],
                full_text=content[:4000],
                language="TypeScript",
                file_path=file_path,
            ))

        return units
