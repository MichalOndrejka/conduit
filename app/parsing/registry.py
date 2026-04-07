from __future__ import annotations

import os
from app.models import CodeUnit, CodeUnitKind
from app.parsing.base import CodeParser
from app.parsing.csharp import CSharpParser
from app.parsing.typescript import TypeScriptParser
from app.parsing.go_parser import GoParser
from app.parsing.powershell import PowerShellParser
from app.parsing.markdown import MarkdownParser


class ParserRegistry:
    def __init__(self) -> None:
        self._parsers: list[CodeParser] = [
            CSharpParser(),
            TypeScriptParser(),
            GoParser(),
            PowerShellParser(),
            MarkdownParser(),
        ]

    def parse(self, content: str, file_path: str) -> list[CodeUnit]:
        ext = os.path.splitext(file_path)[1]
        for parser in self._parsers:
            if parser.can_parse(ext):
                units = parser.parse(content, file_path)
                if units:
                    return units

        # Fallback: index as single text file
        return [CodeUnit(
            kind=CodeUnitKind.FILE,
            name=os.path.basename(file_path),
            full_text=content[:4000],
            language="text",
            file_path=file_path,
        )]
