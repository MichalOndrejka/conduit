# Configuration Reference

Conduit stores its runtime configuration in `config.json` (location controlled by `CONDUIT_CONFIG` env var). All settings are also editable via the **Settings** page in the web UI.

## Full schema

```json
{
  "embedding": {
    "model": "nomic-embed-text-v2-moe",
    "base_url": "http://localhost:11434/v1",
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

Conduit uses [Ollama](https://ollama.ai) for local, private embeddings. The embedding client speaks the OpenAI embeddings API format, so any Ollama-compatible endpoint works.

### `model`

Ollama model identifier. Must match the model name as reported by `ollama list`.

| Recommended model | Dimensions |
|------------------|------------|
| `nomic-embed-text-v2-moe` | `768` |

Pull the model before starting Conduit:

```bash
ollama pull nomic-embed-text-v2-moe
```

### `base_url`

Base URL for the Ollama embeddings endpoint. Default: `http://localhost:11434/v1`.

Change this if Ollama is running on a different host or port.

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

## Changing embedding model

When you change `model` or `dimensions` in Settings:

1. All Qdrant collections are dropped (existing indexed data is lost).
2. All source sync statuses are set to `needs-reindex`.
3. Conduit re-creates collections with the new dimension on the next sync.

Re-sync all sources after changing the embedding model.

---

## Docker deployment

`docker-compose.yml` starts Qdrant and Conduit together. Ollama must be running on the host:

```bash
docker-compose up
```

For colleagues pulling from Docker Hub, use `docker-compose.hub.yml` instead:

```bash
docker-compose -f docker-compose.hub.yml up
```

Mount a volume at `/data` to persist source definitions and config across container restarts.
