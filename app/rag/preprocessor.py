from __future__ import annotations

import logging
from typing import Callable, Optional

import httpx
from openai import AsyncOpenAI

from app.config import AppConfig
from app.models import SourceDocument

log = logging.getLogger(__name__)

_MIN_DOC_LENGTH = 200

_DEFAULT_SYSTEM_PROMPT = (
    "You are a technical documentation assistant. "
    "Summarize the following document as concisely as possible while preserving "
    "all key technical facts, identifiers, error codes, version numbers, and "
    "procedure steps. Respond with only the summary — no preamble, no commentary."
)


class DocumentPreprocessor:
    def __init__(self, cfg: AppConfig) -> None:
        pc = cfg.preprocessing
        self._enabled = pc.enabled
        self._model = pc.model
        self._system_prompt = pc.system_prompt.strip() or _DEFAULT_SYSTEM_PROMPT
        self._source_types: dict[str, bool] = pc.source_types
        self._client: Optional[AsyncOpenAI] = None

        if pc.enabled:
            base_url = pc.base_url or "http://localhost:11434/v1"
            self._client = AsyncOpenAI(base_url=base_url, api_key="ollama", http_client=httpx.AsyncClient())

    @property
    def enabled(self) -> bool:
        return self._enabled

    def enabled_for_type(self, source_type: str) -> bool:
        return self._enabled and self._source_types.get(source_type, True)

    async def preprocess_documents(
        self,
        docs: list[SourceDocument],
        source_type: str = "",
        progress_cb: Optional[Callable[[int, int], None]] = None,
    ) -> list[SourceDocument]:
        if not self._enabled or self._client is None:
            return docs
        if not self._source_types.get(source_type, True):
            log.debug("Preprocessing skipped for source type '%s'", source_type)
            return docs

        result: list[SourceDocument] = []
        total = len(docs)

        for i, doc in enumerate(docs):
            if len(doc.text) < _MIN_DOC_LENGTH:
                result.append(doc)
            else:
                summarized = await self._summarize(doc)
                result.append(doc.model_copy(update={"text": summarized}))

            if progress_cb:
                progress_cb(i + 1, total)

        return result

    async def _summarize(self, doc: SourceDocument) -> str:
        try:
            response = await self._client.chat.completions.create(
                model=self._model,
                messages=[
                    {"role": "system", "content": self._system_prompt},
                    {"role": "user", "content": doc.text},
                ],
            )
            summary = (response.choices[0].message.content or "").strip()
            if not summary:
                log.warning("Preprocessor returned empty summary for doc %s — keeping original", doc.id)
                return doc.text
            return summary
        except Exception:
            log.warning(
                "Preprocessing LLM call failed for doc %s — keeping original text",
                doc.id,
                exc_info=True,
            )
            return doc.text
