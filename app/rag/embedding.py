from __future__ import annotations

import logging
import os

from openai import AsyncOpenAI

from app.config import AppConfig

log = logging.getLogger(__name__)


class EmbeddingService:
    def __init__(self, cfg: AppConfig) -> None:
        ec = cfg.embedding
        api_key = os.environ.get(ec.api_key_env_var, "nokey")

        if ec.provider == "ollama":
            base_url = ec.base_url or "http://localhost:11434/v1"
            self._client = AsyncOpenAI(base_url=base_url, api_key="ollama")
        elif ec.provider == "openai-compatible":
            self._client = AsyncOpenAI(base_url=ec.base_url or None, api_key=api_key)
        else:
            self._client = AsyncOpenAI(api_key=api_key)

        self._model      = ec.model
        self._dimensions = ec.dimensions
        self._provider   = ec.provider
        self._max_chars  = ec.max_input_chars  # 0 = unlimited

    async def embed(self, text: str) -> list[float]:
        """Embed text, truncating to max_input_chars as a hard safety net."""
        if self._max_chars and len(text) > self._max_chars:
            log.warning(
                "Truncating input from %d to %d chars before embedding "
                "(increase max_input_chars in config if this is unexpected)",
                len(text), self._max_chars,
            )
            # Truncate at a newline boundary when possible to avoid splitting mid-token
            truncated = text[: self._max_chars]
            last_nl = truncated.rfind("\n")
            if last_nl > self._max_chars // 2:
                truncated = truncated[:last_nl]
            text = truncated

        kwargs: dict = {"model": self._model, "input": text}
        # The OpenAI API supports a `dimensions` parameter for matryoshka models.
        # Ollama and generic openai-compatible providers may not, so only pass it for native OpenAI.
        if self._provider == "openai":
            kwargs["dimensions"] = self._dimensions

        response = await self._client.embeddings.create(**kwargs)
        return response.data[0].embedding
