from __future__ import annotations

import logging

import httpx
from openai import AsyncOpenAI

from app.config import AppConfig

log = logging.getLogger(__name__)


class EmbeddingService:
    def __init__(self, cfg: AppConfig) -> None:
        ec = cfg.embedding
        base_url = ec.base_url or "http://localhost:11434/v1"
        self._client = AsyncOpenAI(base_url=base_url, api_key="ollama", http_client=httpx.AsyncClient())
        self._model      = ec.model
        self._dimensions = ec.dimensions
        self._max_chars  = ec.max_input_chars  # 0 = unlimited

    async def embed(self, text: str) -> list[float]:
        if self._max_chars and len(text) > self._max_chars:
            log.warning(
                "Truncating input from %d to %d chars before embedding "
                "(increase max_input_chars in config if this is unexpected)",
                len(text), self._max_chars,
            )
            truncated = text[: self._max_chars]
            last_nl = truncated.rfind("\n")
            if last_nl > self._max_chars // 2:
                truncated = truncated[:last_nl]
            text = truncated

        response = await self._client.embeddings.create(model=self._model, input=text)
        return response.data[0].embedding
