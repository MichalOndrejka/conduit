# Configuration Reference

Conduit stores its runtime configuration in `config.json` (location controlled by `CONDUIT_CONFIG` env var). All settings are also editable via the **Settings** page in the web UI.

## Full schema

```json
{
  "embedding": {
    "provider": "ollama",
    "model": "nomic-embed-text-v2-moe",
    "base_url": "http://localhost:11434/v1",
    "api_key_env_var": "",
    "dimensions": 768,
    "max_input_chars": 8000
  },
  "qdrant": {
    "host": "localhost",
    "port": 6333
  },
  "chunking": {
    "max_chunk_size": 2000,
    "overlap": 200
  },
  "sources_file_path": "conduit-sources.json"
}
```

---

## Embedding

### `provider`

| Value | Description |
|-------|-------------|
| `ollama` | Local Ollama instance (default). No API key needed. |
| `openai` | OpenAI embeddings API. |
| `openai-compatible` | Any endpoint that speaks the OpenAI embeddings API format. |

### `model`

Model identifier string. Must match what the provider expects.

| Provider | Recommended model | Dimensions |
|----------|------------------|------------|
| `ollama` | `nomic-embed-text-v2-moe` | `768` |
| `openai` | `text-embedding-3-small` | `1536` |
| `openai-compatible` | Provider-specific | Provider-specific |

### `api_key_env_var`

Name of the environment variable holding the API key. Leave empty for Ollama. For OpenAI, set to `OPENAI_API_KEY` and export that variable.

### `base_url`

Base URL for the embeddings endpoint. Required for Ollama and `openai-compatible` providers. Leave empty for the standard OpenAI endpoint.

### `dimensions`

Vector dimensions. **Must match the model exactly.** Changing this drops all existing Qdrant collections and marks every source for re-indexing.

### `max_input_chars`

Hard limit on characters sent to the embedding API per chunk. Default `8000` (~2 000 tokens). Chunks exceeding this are truncated before embedding. Increase if your model supports a larger context window and your documents are dense.

---

## Qdrant

### `host` / `port`

Qdrant connection details. Override with `QDRANT_HOST` / `QDRANT_PORT` environment variables (useful in Docker where the service name differs from `localhost`).

---

## Chunking

### `max_chunk_size`

Maximum characters per chunk. The chunker splits at sentence boundaries or newlines to stay under this limit. Default `2000`.

### `overlap`

Characters of overlap between consecutive chunks. Overlap preserves context at chunk boundaries. Default `200`. Set to `0` to disable.

---

## `sources_file_path`

Path to the JSON file where source definitions are persisted. Defaults to `conduit-sources.json` in the current working directory. Use `CONDUIT_DATA_DIR` to redirect both this file and `config.json` to a shared data directory.

---

## Environment variables

| Variable | Effect |
|----------|--------|
| `CONDUIT_CONFIG` | Path to `config.json`. Default: `config.json` in CWD. |
| `CONDUIT_DATA_DIR` | If set, `config.json` and `conduit-sources.json` are placed here. Useful for Docker volumes. |
| `QDRANT_HOST` | Overrides `qdrant.host` in config. |
| `QDRANT_PORT` | Overrides `qdrant.port` in config. |
| Any name | Can be used as a credential reference in source config fields (`Pat`, `Token`, `Password`, `ApiKeyValue`). |

---

## Embedding providers

### Ollama (default)

Run Ollama locally and pull a model:

```bash
ollama pull nomic-embed-text-v2-moe
```

Config:
```json
{
  "embedding": {
    "provider": "ollama",
    "model": "nomic-embed-text-v2-moe",
    "base_url": "http://localhost:11434/v1",
    "dimensions": 768
  }
}
```

### OpenAI

```bash
export OPENAI_API_KEY="sk-..."
```

Config:
```json
{
  "embedding": {
    "provider": "openai",
    "model": "text-embedding-3-small",
    "api_key_env_var": "OPENAI_API_KEY",
    "dimensions": 1536
  }
}
```

### Azure OpenAI

```bash
export AZURE_OPENAI_KEY="..."
```

Config:
```json
{
  "embedding": {
    "provider": "openai-compatible",
    "model": "text-embedding-3-small",
    "base_url": "https://<resource>.openai.azure.com/openai/deployments/<deployment>",
    "api_key_env_var": "AZURE_OPENAI_KEY",
    "dimensions": 1536
  }
}
```

### Any OpenAI-compatible endpoint

```json
{
  "embedding": {
    "provider": "openai-compatible",
    "model": "your-model-name",
    "base_url": "https://your-endpoint/v1",
    "api_key_env_var": "YOUR_KEY_VAR",
    "dimensions": 1536
  }
}
```

---

## Changing embedding model

When you change `model` or `dimensions` in Settings:

1. All Qdrant collections are dropped (existing indexed data is lost).
2. All source sync statuses are set to `needs-reindex`.
3. Conduit re-creates collections with the new dimension on the next sync.

Re-sync all sources after changing the embedding model.

---

## Docker deployment

`docker-compose.yml` starts Qdrant and Conduit together. Pass configuration via environment:

```bash
OPENAI_API_KEY=sk-... \
QDRANT_HOST=qdrant \
CONDUIT_DATA_DIR=/data \
docker-compose up
```

Mount a volume at `/data` to persist source definitions and config across container restarts.
