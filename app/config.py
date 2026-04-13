from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Optional
from pydantic import BaseModel


# Per-provider defaults shown in the UI when the user switches provider
PROVIDER_DEFAULTS: dict[str, dict] = {
    "openai":             {"model": "text-embedding-3-small", "dimensions": 1536},
    "ollama":             {"model": "nomic-embed-text-v2-moe", "dimensions": 768},
    "openai-compatible":  {"model": "",                        "dimensions": 1536},
}


class EmbeddingConfig(BaseModel):
    provider: str = "ollama"          # openai | ollama | openai-compatible
    model: str = "nomic-embed-text-v2-moe"
    api_key_env_var: str = ""
    base_url: str = ""
    dimensions: int = 768
    max_input_chars: int = 8000       # Hard cap on chars sent to the API (≈2 000 tokens for dense code)
    verify_ssl: str = "true"          # "true" | "false" | path to CA bundle


class QdrantConfig(BaseModel):
    host: str = "localhost"
    port: int = 6333


class ChunkingConfig(BaseModel):
    max_chunk_size: int = 2000
    overlap: int = 200


class AppConfig(BaseModel):
    embedding: EmbeddingConfig = EmbeddingConfig()
    qdrant: QdrantConfig = QdrantConfig()
    chunking: ChunkingConfig = ChunkingConfig()
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
