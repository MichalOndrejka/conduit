from __future__ import annotations

import re
from app.models import CodeUnit, CodeUnitKind
from app.parsing.base import CodeParser

_NAMESPACE_RE = re.compile(r"^\s*namespace\s+([\w.]+)", re.MULTILINE)
_DOC_COMMENT_RE = re.compile(r"((?:\s*///[^\n]*\n)+)", re.MULTILINE)
_XMLDOC_CONTENT_RE = re.compile(r"///\s?(.+)")

# Type declarations
_TYPE_RE = re.compile(
    r"(?P<doc>(?:\s*///[^\n]*\n)*)"
    r"\s*(?P<access>public|internal|private|protected(?:\s+internal)?|file)?\s*"
    r"(?:(?:abstract|sealed|static|partial|readonly)\s+)*"
    r"(?P<kind>class|interface|record|struct|enum)\s+(?P<name>\w+)",
    re.MULTILINE,
)

# Method/constructor/property
_MEMBER_RE = re.compile(
    r"(?P<doc>(?:\s*///[^\n]*\n)*)"
    r"\s*(?P<access>public|internal|private|protected(?:\s+internal)?|file)?\s*"
    r"(?:(?:abstract|virtual|override|static|async|new|readonly|sealed|partial)\s+)*"
    r"(?P<return_type>[\w<>\[\]?,\s]+?)\s+"
    r"(?P<name>\w+)"
    r"(?P<params>\([^)]*\))"
    r"(?:\s*:\s*(?:base|this)\([^)]*\))?"
    r"\s*(?:\{|=>|;)",
    re.MULTILINE,
)


def _extract_doc(raw: str) -> str:
    lines = [m.group(1).strip() for m in _XMLDOC_CONTENT_RE.finditer(raw)]
    cleaned: list[str] = []
    for line in lines:
        # Strip XML tags
        line = re.sub(r"<[^>]+>", "", line).strip()
        if line:
            cleaned.append(line)
    return " ".join(cleaned)


def _extract_block(lines: list[str], start: int) -> tuple[int, str]:
    """Extract a brace-delimited block starting at or after start line."""
    depth = 0
    block_lines: list[str] = []
    in_block = False

    for i in range(start, len(lines)):
        line = lines[i]
        block_lines.append(line)
        for ch in line:
            if ch == "{":
                depth += 1
                in_block = True
            elif ch == "}":
                depth -= 1
        if in_block and depth == 0:
            return i, "\n".join(block_lines)

    return len(lines) - 1, "\n".join(block_lines)


class CSharpParser(CodeParser):
    def can_parse(self, extension: str) -> bool:
        return extension.lower() == ".cs"

    def parse(self, content: str, file_path: str) -> list[CodeUnit]:
        try:
            return self._parse(content, file_path)
        except Exception:
            return []

    def _parse(self, content: str, file_path: str) -> list[CodeUnit]:
        units: list[CodeUnit] = []
        lines = content.splitlines()

        # Detect namespace
        ns_match = _NAMESPACE_RE.search(content)
        namespace = ns_match.group(1) if ns_match else None

        # Find type declarations
        current_type: str | None = None
        current_type_kind: CodeUnitKind = CodeUnitKind.CLASS

        for m in _TYPE_RE.finditer(content):
            is_public = m.group("access") in ("public", None)
            if m.group("access") is None:
                is_public = True  # default for top-level

            kind_str = m.group("kind").lower()
            kind = {
                "class": CodeUnitKind.CLASS,
                "interface": CodeUnitKind.INTERFACE,
                "record": CodeUnitKind.RECORD,
                "struct": CodeUnitKind.STRUCT,
                "enum": CodeUnitKind.ENUM,
            }.get(kind_str, CodeUnitKind.CLASS)

            name = m.group("name")
            doc = _extract_doc(m.group("doc") or "")

            # Get the block text
            start_line = content[:m.start()].count("\n")
            end_line, block_text = _extract_block(lines, start_line)

            units.append(CodeUnit(
                kind=kind,
                name=name,
                namespace=namespace,
                is_public=is_public,
                doc_comment=doc or None,
                full_text=block_text[:4000],  # cap large types
                language="C#",
                file_path=file_path,
            ))

            current_type = name
            current_type_kind = kind

        # Find methods/constructors/properties within types
        for m in _MEMBER_RE.finditer(content):
            name = m.group("name")
            if name in ("if", "while", "for", "foreach", "switch", "using", "return", "new", "catch"):
                continue

            access = m.group("access") or ""
            is_public = access in ("public", "")
            return_type = m.group("return_type").strip()
            params = m.group("params")
            doc = _extract_doc(m.group("doc") or "")

            # Detect constructor (return type matches a known type name)
            # Simple heuristic: if return_type contains no spaces and matches a known type
            kind = CodeUnitKind.METHOD

            signature = f"{return_type} {name}{params}"
            start_line = content[:m.start()].count("\n")
            end_line, block_text = _extract_block(lines, start_line)

            units.append(CodeUnit(
                kind=kind,
                name=name,
                container_name=current_type,
                namespace=namespace,
                signature=signature,
                is_public=is_public,
                doc_comment=doc or None,
                full_text=block_text[:3000],
                language="C#",
                file_path=file_path,
            ))

        if not units:
            # Return file as a single unit
            units.append(CodeUnit(
                kind=CodeUnitKind.FILE,
                name=file_path.split("/")[-1].split("\\")[-1],
                namespace=namespace,
                full_text=content[:4000],
                language="C#",
                file_path=file_path,
            ))

        return units
