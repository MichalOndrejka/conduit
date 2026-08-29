# Configuration Reference

Conduit stores its runtime configuration in `config.json` (location controlled by `CONDUIT_CONFIG` env var). All settings are also editable via the **Settings** page in the web UI.

## Full schema

```json
{
  "embedding": {
    "model": "nomic-embed-text-v2-moe",
    "base_url": "http://localhost:11434/v1",
    "dimensions": 768,
    "max_input_tokens": 8192,
    "concurrency": 4
  },
  "qdrant": {
    "url": "http://localhost:6333",
    "api_key": ""
  },
  "chunking": {
    "max_chunk_size": 2000,
    "overlap": 200
  },
  "preprocessing": {
    "enabled": false,
    "base_url": "",
    "model": "",
    "system_prompt": "",
    "source_types": {
      "work-item": true, "requirements": true, "test-case": true,
      "commit-history": false, "code": false,
      "test-code": false, "documentation": true
    },
    "concurrency": 4
  },
  "sources_file_path": "conduit-sources.json"
}
```

---

## Embedding

Conduit talks to any endpoint speaking the OpenAI embeddings API format, such
as [Ollama](https://ollama.ai), for local, private embeddings. There's no
cloud provider option — only local/self-hosted OpenAI-compatible endpoints
are supported.

#### `model`

Model identifier. For Ollama, must match the model name as reported by `ollama list`.

| Recommended model | Dimensions |
|------------------|------------|
| `nomic-embed-text-v2-moe` | `768` |

Pull the model before starting Conduit:

```bash
ollama pull nomic-embed-text-v2-moe
```

#### `base_url`

Base URL for the OpenAI-compatible embeddings endpoint. Default: `http://localhost:11434/v1`.

Change this if Ollama is running on a different host or port. Override with the `EMBEDDING_BASE_URL` env var.

### `dimensions`

Vector dimensions. **Must match the model exactly.** Changing this drops all existing Qdrant collections and marks every source for re-indexing. Override with `EMBEDDING_DIMENSIONS`.

### `max_input_tokens`

Context window of your embedding model in tokens. Default `8192` (matches `nomic-embed-text-v2-moe`; `text-embedding-3-small`/`-large` use `8191`). Check your model's card for the correct value. The app converts this to a character limit using a conservative 2 chars/token estimate, so chunks are never sent to the model in excess of its context window. Override with `EMBEDDING_MAX_INPUT_TOKENS`.

---

## Qdrant

### `url`

Full Qdrant connection URL, including scheme, e.g. `http://localhost:6333` or
`https://my-cluster.qdrant.io:6333` for Qdrant Cloud. Override with the
`QDRANT_URL` environment variable (useful in Docker where the service name
differs from `localhost`).

### `api_key`

Optional API key sent with every Qdrant request. Required for Qdrant Cloud or
a self-hosted Qdrant with `QDRANT__SERVICE__API_KEY` set. Override with
`QDRANT_API_KEY`.

---

## Chunking

### `max_chunk_size`

Maximum characters per chunk. The chunker splits at sentence boundaries or newlines to stay under this limit. Default `2000`.

### `overlap`

Characters of overlap between consecutive chunks. Overlap preserves context at chunk boundaries. Default `200`. Set to `0` to disable.

---

## Preprocessing (optional LLM summarization)

Runs at sync time, per source type, before chunking/embedding.

### `enabled`

Master switch, toggled on the **Settings** page. Default `false`.

### `base_url` / `model` / `system_prompt`

Chat endpoint, model name, and system prompt used to summarize documents. `system_prompt` defaults to a built-in technical-summarization prompt if left empty. Override `base_url` with `PREPROCESSING_BASE_URL` and `model` with `PREPROCESSING_MODEL`.

### `source_types`

Map of source type key (`work-item`, `requirements`, `test-case`, `commit-history`, `code`, `test-code`, `documentation`) → whether preprocessing runs for that type. A type absent from the map defaults to enabled. Documents shorter than 200 characters are always passed through unsummarized.

Like embedding, preprocessing talks to any OpenAI-compatible chat endpoint (e.g. Ollama) — there's no separate cloud provider option.

---

## `sources_file_path`

Path to the JSON file where source definitions are persisted. Defaults to `conduit-sources.json` in the current working directory. Use `CONDUIT_DATA_DIR` to redirect both this file and `config.json` to a shared data directory.

---

## Environment variables

| Variable | Effect |
|----------|--------|
| `CONDUIT_CONFIG` | Path to `config.json`. Default: `config.json` in CWD. |
| `CONDUIT_DATA_DIR` | If set, `config.json`, `conduit-sources.json`, and `credentials.enc.json` are placed here. **Required in Docker** to persist all data across container restarts. |
| `CONDUIT_SECRET_KEY` | Base64url Fernet key for encrypting `credentials.enc.json`. Auto-generated and stored as `.secret_key` inside the data directory if not provided. Set this explicitly in production so credentials survive container recreation. |
| `QDRANT_URL` | Overrides `qdrant.url` in config. |
| `QDRANT_API_KEY` | Overrides `qdrant.api_key`. |
| `EMBEDDING_MODEL` | Overrides `embedding.model`. |
| `EMBEDDING_BASE_URL` | Overrides `embedding.base_url`. |
| `EMBEDDING_DIMENSIONS` | Overrides `embedding.dimensions`. |
| `EMBEDDING_MAX_INPUT_TOKENS` | Overrides `embedding.max_input_tokens`. |
| `EMBEDDING_CONCURRENCY` | Overrides `embedding.concurrency` — max in-flight embed calls during a sync. Default: `4`. |
| `PREPROCESSING_MODEL` | Overrides `preprocessing.model`. |
| `PREPROCESSING_BASE_URL` | Overrides `preprocessing.base_url`. |
| `PREPROCESSING_CONCURRENCY` | Overrides `preprocessing.concurrency` — max in-flight preprocessing/chat calls during a sync. Default: `4`. |
| `CONDUIT_HOST` | Address the HTTP server binds to. Default: `127.0.0.1`. Set to `0.0.0.0` in Docker so the port mapping works. |
| `PORT` | HTTP listen port. Default: `8000`. |

Every variable above always overrides the matching `config.json` value on process
start, including on a container restart — if it's set in the environment, a value
saved for the same field from the **Settings** page has no effect until that env
var is unset.

---

## No built-in authentication

Conduit has no login or access control — anyone who can reach the app can
view and change settings, manage credentials and sources, trigger syncs, and
query indexed data via `/mcp`. It's designed to run as a local container
reachable only from `localhost` or a trusted network; don't expose it
directly to the internet.

---

## Changing embedding model

When you change `model` or `dimensions` in Settings:

1. All Qdrant collections are dropped (existing indexed data is lost).
2. All source sync statuses are set to `needs-reindex`.
3. Conduit re-creates collections with the new dimension on the next sync.

Re-sync all sources after changing the embedding model.

---

## Docker deployment

Conduit runs as a standalone container and connects out to Qdrant and your
embedding/LLM endpoint(s), which you run separately — it doesn't bundle or
manage those services:

```bash
docker build -t conduit .
docker run -d --name conduit -p 8000:8000 \
  -v conduit_data:/data \
  -e CONDUIT_HOST=0.0.0.0 \
  -e QDRANT_URL=http://host.docker.internal:6333 \
  -e EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1 \
  conduit
```

Or pull the prebuilt image instead of building:

```bash
docker run -d --name conduit -p 8000:8000 \
  -v conduit_data:/data \
  -e CONDUIT_HOST=0.0.0.0 \
  -e QDRANT_URL=http://host.docker.internal:6333 \
  -e EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1 \
  michalondrejka/conduit:latest
```

`host.docker.internal` reaches services running on the host; point the env
vars at wherever Qdrant/Ollama actually run instead, or skip them
and set the connection details from the **Settings** page after the container
starts. The volume at `/data` and `CONDUIT_DATA_DIR=/data` persist source
definitions, config, and credentials across container restarts.

To keep the credential encryption key stable across container recreation, add `CONDUIT_SECRET_KEY` to your environment. It must be a base64url-encoded 32-byte key (Fernet format) — generate one with OpenSSL:

```bash
# generate once and store safely
openssl rand -base64 32 | tr '+/' '-_'
```

Then pass it to `docker run`:

```bash
-e CONDUIT_SECRET_KEY=<generated key>
```

Without this, Conduit auto-generates a key saved as `.secret_key` inside `/data`. As long as the volume is preserved, credentials remain readable — but if the volume is deleted and recreated, existing `credentials.enc.json` data will be unreadable.

---

## Credential library

Secrets (PATs, tokens, API keys) are managed on the **`/credentials`** page in the web UI, not as environment variables. Each credential has a name; the secret value is Fernet-encrypted before being written to disk.

Source config fields (`Pat`, `Token`, `Password`, `ApiKeyValue`) store the credential name. At sync time, Conduit looks up the name in the in-memory cache, decrypts the value, and passes it to the HTTP client — the plaintext secret never appears in `conduit-sources.json` or the config file.

**Cross-system portability**: credential names are stable identifiers. When you export sources and import them on another machine, Conduit will list any referenced-but-missing credentials on the `/credentials` page so you can create them before syncing.
