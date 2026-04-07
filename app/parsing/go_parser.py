from __future__ import annotations

import re
from app.models import CodeUnit, CodeUnitKind
from app.parsing.base import CodeParser

_PACKAGE_RE = re.compile(r"^package\s+(\w+)", re.MULTILINE)
_FUNC_RE = re.compile(
    r"^func\s+(?:\((?P<receiver>[^)]+)\)\s+)?(?P<name>\w+)\s*\((?P<params>[^)]*)\)",
    re.MULTILINE,
)
_STRUCT_RE = re.compile(r"^type\s+(?P<name>\w+)\s+struct\b", re.MULTILINE)
_INTERFACE_RE = re.compile(r"^type\s+(?P<name>\w+)\s+interface\b", re.MULTILINE)
_COMMENT_BLOCK_RE = re.compile(r"((?:\s*//[^\n]*\n)+)$")


def _preceding_comment(content: str, match_start: int) -> str:
    before = content[:match_start]
    m = _COMMENT_BLOCK_RE.search(before)
    if m:
        lines = [re.sub(r"^\s*//\s?", "", l) for l in m.group(1).splitlines()]
        return " ".join(l for l in lines if l.strip())
    return ""


def _extract_block(content: str, start: int, max_lines: int = 80) -> str:
    return "\n".join(content[start:].splitlines()[:max_lines])


class GoParser(CodeParser):
    def can_parse(self, extension: str) -> bool:
        return extension.lower() == ".go"

    def parse(self, content: str, file_path: str) -> list[CodeUnit]:
        try:
            return self._parse(content, file_path)
        except Exception:
            return []

    def _parse(self, content: str, file_path: str) -> list[CodeUnit]:
        units: list[CodeUnit] = []

        pkg_match = _PACKAGE_RE.search(content)
        namespace = pkg_match.group(1) if pkg_match else None

        for m in _FUNC_RE.finditer(content):
            name = m.group("name")
            receiver_raw = m.group("receiver") or ""
            receiver = re.sub(r"\*", "", receiver_raw.split()[-1]).strip() if receiver_raw else None
            doc = _preceding_comment(content, m.start())
            params = m.group("params")
            block = _extract_block(content, m.start())

            units.append(CodeUnit(
                kind=CodeUnitKind.FUNCTION,
                name=name,
                container_name=receiver,
                namespace=namespace,
                signature=f"func {name}({params})",
                is_public=name[0].isupper() if name else False,
                doc_comment=doc or None,
                full_text=block[:3000],
                language="Go",
                file_path=file_path,
            ))

        for pattern, kind in [(_STRUCT_RE, CodeUnitKind.STRUCT), (_INTERFACE_RE, CodeUnitKind.INTERFACE)]:
            for m in pattern.finditer(content):
                name = m.group("name")
                doc = _preceding_comment(content, m.start())
                block = _extract_block(content, m.start())
                units.append(CodeUnit(
                    kind=kind,
                    name=name,
                    namespace=namespace,
                    is_public=name[0].isupper() if name else False,
                    doc_comment=doc or None,
                    full_text=block[:3000],
                    language="Go",
                    file_path=file_path,
                ))

        if not units:
            units.append(CodeUnit(
                kind=CodeUnitKind.FILE,
                name=file_path.split("/")[-1].split("\\")[-1],
                namespace=namespace,
                full_text=content[:4000],
                language="Go",
                file_path=file_path,
            ))

        return units
