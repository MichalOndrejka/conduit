from __future__ import annotations

import logging

import httpx
from openai import AsyncOpenAI

from app.config import AppConfig

log = logging.getLogger(__name__)

# Conservative chars-per-token estimate. Real ratio is 3–4 for English, 2–3 for code.
# Using 2 keeps us safely below the token limit for all ASCII-heavy content.
_CHARS_PER_TOKEN = 2


class EmbeddingService:
    def __init__(self, cfg: AppConfig) -> None:
        ec = cfg.embedding
        base_url = ec.base_url
        self._client = AsyncOpenAI(base_url=base_url, api_key="ollama", http_client=httpx.AsyncClient())
        self._model      = ec.model
        self._dimensions = ec.dimensions
        self._max_tokens = ec.max_input_tokens
        self._max_chars  = self._max_tokens * _CHARS_PER_TOKEN

    async def embed(self, text: str) -> list[float]:
        if len(text) > self._max_chars:
            log.warning(
                "Truncating input from %d to %d chars before embedding "
                "(model limit: %d tokens × %d chars/token estimate — update max_input_tokens in config if needed)",
                len(text), self._max_chars, self._max_tokens, _CHARS_PER_TOKEN,
            )
            truncated = text[: self._max_chars]
            last_nl = truncated.rfind("\n")
            if last_nl > self._max_chars // 2:
                truncated = truncated[:last_nl]
            text = truncated

        response = await self._client.embeddings.create(model=self._model, input=text)
        return response.data[0].embedding
