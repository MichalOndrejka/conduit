from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Optional
from pydantic import BaseModel


class EmbeddingConfig(BaseModel):
    provider: str = "openai-compatible"  # "openai-compatible" (Ollama, etc.) | "azure-openai"
    model: str = "nomic-embed-text-v2-moe"
    base_url: str = "http://localhost:11434/v1"
    dimensions: int = 768
    max_input_tokens: int = 8192      # Embedding model's context window in tokens (check model card)
    # Azure OpenAI — only used when provider == "azure-openai"
    azure_endpoint: str = ""          # e.g. https://my-resource.openai.azure.com/
    azure_deployment: str = ""        # name of the model deployment (e.g. "text-embedding-3-small")
    azure_api_version: str = "2024-02-01"
    azure_api_key_credential: str = ""  # name of a credential in the secrets store holding the API key


class QdrantConfig(BaseModel):
    host: str = "localhost"
    port: int = 6333
    https: bool = False
    api_key: str = ""


class ChunkingConfig(BaseModel):
    max_chunk_size: int = 2000
    overlap: int = 200


_PREPROCESS_SOURCE_TYPE_DEFAULTS: dict[str, bool] = {
    "workitem":      True,
    "requirements":  True,
    "test-case":     True,
    "test-results":  True,
    "git-commits":   False,
    "code":          False,
    "testcode":      False,
    "pipeline-build": True,
    "documentation": True,
}


class PreprocessingConfig(BaseModel):
    enabled: bool = False
    base_url: str = ""          # Ollama base URL; defaults to http://localhost:11434/v1
    model: str = ""
    system_prompt: str = ""     # empty = built-in summarization prompt
    source_types: dict[str, bool] = _PREPROCESS_SOURCE_TYPE_DEFAULTS.copy()


class AppConfig(BaseModel):
    embedding: EmbeddingConfig = EmbeddingConfig()
    qdrant: QdrantConfig = QdrantConfig()
    chunking: ChunkingConfig = ChunkingConfig()
    preprocessing: PreprocessingConfig = PreprocessingConfig()
    sources_file_path: str = "conduit-sources.json"


_CONFIG_PATH = Path(os.environ.get("CONDUIT_CONFIG", "config.json"))
_config: Optional[AppConfig] = None


def load_config() -> AppConfig:
    global _config
    if _CONFIG_PATH.exists():
        data = json.loads(_CONFIG_PATH.read_text())
        _config = AppConfig.model_validate(data)
    else:
        _config = AppConfig()
    # Allow env vars to override qdrant connection and data dir (useful in Docker)
    if os.environ.get("QDRANT_HOST"):
        _config.qdrant.host = os.environ["QDRANT_HOST"]
    if os.environ.get("QDRANT_PORT"):
        _config.qdrant.port = int(os.environ["QDRANT_PORT"])
    if os.environ.get("QDRANT_HTTPS"):
        _config.qdrant.https = os.environ["QDRANT_HTTPS"].strip().lower() in ("1", "true", "yes")
    if os.environ.get("QDRANT_API_KEY"):
        _config.qdrant.api_key = os.environ["QDRANT_API_KEY"]
    # Allow env vars to configure embedding (useful for cloud deploys with no mounted config.json)
    if os.environ.get("EMBEDDING_PROVIDER"):
        _config.embedding.provider = os.environ["EMBEDDING_PROVIDER"]
    if os.environ.get("EMBEDDING_MODEL"):
        _config.embedding.model = os.environ["EMBEDDING_MODEL"]
    if os.environ.get("EMBEDDING_BASE_URL"):
        _config.embedding.base_url = os.environ["EMBEDDING_BASE_URL"]
    if os.environ.get("EMBEDDING_DIMENSIONS"):
        _config.embedding.dimensions = int(os.environ["EMBEDDING_DIMENSIONS"])
    if os.environ.get("EMBEDDING_MAX_INPUT_TOKENS"):
        _config.embedding.max_input_tokens = int(os.environ["EMBEDDING_MAX_INPUT_TOKENS"])
    if os.environ.get("AZURE_OPENAI_ENDPOINT"):
        _config.embedding.azure_endpoint = os.environ["AZURE_OPENAI_ENDPOINT"]
    if os.environ.get("AZURE_OPENAI_DEPLOYMENT"):
        _config.embedding.azure_deployment = os.environ["AZURE_OPENAI_DEPLOYMENT"]
    if os.environ.get("AZURE_OPENAI_API_VERSION"):
        _config.embedding.azure_api_version = os.environ["AZURE_OPENAI_API_VERSION"]
    if os.environ.get("CONDUIT_DATA_DIR"):
        data_dir = Path(os.environ["CONDUIT_DATA_DIR"])
        data_dir.mkdir(parents=True, exist_ok=True)
        if not Path(_config.sources_file_path).is_absolute():
            _config.sources_file_path = str(data_dir / Path(_config.sources_file_path).name)
    return _config


def get_config() -> AppConfig:
    if _config is None:
        return load_config()
    return _config


def save_config(cfg: AppConfig) -> None:
    global _config
    _CONFIG_PATH.write_text(
        json.dumps(cfg.model_dump(), indent=2),
        encoding="utf-8",
    )
    _config = cfg


def get_config_path() -> str:
    return str(_CONFIG_PATH.resolve())
