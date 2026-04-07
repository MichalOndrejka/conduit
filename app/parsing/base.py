from __future__ import annotations

from abc import ABC, abstractmethod
from app.models import CodeUnit


class CodeParser(ABC):
    @abstractmethod
    def can_parse(self, extension: str) -> bool: ...

    @abstractmethod
    def parse(self, content: str, file_path: str) -> list[CodeUnit]: ...
