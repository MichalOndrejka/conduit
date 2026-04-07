from __future__ import annotations

import re
from app.config import AppConfig
from app.models import TextChunk


_SENTENCE_END = re.compile(r"[.!?]\s+")
_NEWLINE      = re.compile(r"\n")


class TextChunker:
    def __init__(self, cfg: AppConfig) -> None:
        self._max_size = cfg.chunking.max_chunk_size
        self._overlap  = cfg.chunking.overlap

    def chunk(self, text: str) -> list[TextChunk]:
        """
        Split text into overlapping chunks no larger than max_size characters.

        Break priority:
          1. Last sentence boundary ([.!?] followed by whitespace) inside the window
          2. Last newline inside the window — natural for code where sentence breaks are rare
          3. Hard character cut — absolute fallback so a chunk is never > max_size chars

        This ensures code with long methods never produces a chunk that exceeds the
        configured size, regardless of whether natural sentence boundaries exist.
        """
        if not text:
            return []

        # Fast path: fits in one chunk
        if len(text) <= self._max_size:
            return [TextChunk(text=text, index=0, start_offset=0, end_offset=len(text))]

        chunks: list[TextChunk] = []
        start = 0
        idx   = 0

        while start < len(text):
            end = min(start + self._max_size, len(text))

            if end < len(text):
                window = text[start:end]

                # Priority 1: last sentence boundary
                best_break = None
                for m in _SENTENCE_END.finditer(window):
                    best_break = m.end()

                # Priority 2: last newline (crucial for code — keeps lines intact)
                if best_break is None:
                    for m in _NEWLINE.finditer(window):
                        best_break = m.end()

                # Reject a break that's too close to the start (< 25% of window)
                # to avoid tiny slivers that stall progress.
                min_break = max(1, self._max_size // 4)
                if best_break is not None and best_break < min_break:
                    best_break = None  # fall through to hard cut

                if best_break is not None:
                    end = start + best_break

            chunk_text = text[start:end].strip()
            if chunk_text:
                chunks.append(TextChunk(
                    text=chunk_text,
                    index=idx,
                    start_offset=start,
                    end_offset=end,
                ))
                idx += 1

            if end >= len(text):
                break

            # Advance with overlap — always move forward by at least 1 char
            start = max(start + 1, end - self._overlap)

        return chunks
