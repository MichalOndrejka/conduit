# Conduit

![Go 1.24+](https://img.shields.io/badge/go-1.24%2B-00ADD8?logo=go)
![Docker](https://img.shields.io/badge/docker-michalondrejka%2Fconduit-blue?logo=docker)
![License](https://img.shields.io/badge/license-MIT-green)

A **RAG + MCP server** that gives any MCP client semantic search over your data — work items, code, docs from **any JSON-over-HTTP API** — plus a persistent **Experience** store for remembering facts, preferences, and past decisions across sessions.

Conduit is a single ~13 MB Go binary (≈25 MB distroless image) backed by Qdrant and any OpenAI-compatible embedding endpoint.

## Features

- **7 MCP search tools** covering work items, requirements, source code, test code, test cases, commits, and documentation
- **Provider-agnostic sources** — connect any JSON API through configuration (URL, auth, field mappings, pagination); no provider-specific code. Azure DevOps, Jira, GitHub, internal tools — all just configuration
- **Persistent memory** across sessions via the Experience store (`remember` / `retrieve_experience`)
- **Web UI** for managing sources, credentials, triggering syncs, and a PCA vector map
- **Encrypted credential library** — secrets stored Fernet-encrypted, referenced by name; never in config files or exports
- **Sync controls** — pause, resume, or cancel a running sync with live progress
- **Transactional indexing** — failed syncs leave existing data intact

## How it works

```
MCP Client ──► MCP Tools (/mcp) ──► Qdrant Vector Store
                                       ▲
Web UI ──► Sync engine ──► generic API / manual sources
```

**Indexing pipeline**
1. Fetch documents from configured sources (any JSON endpoint, or pasted content)
2. Chunk text with sentence-boundary and newline-priority splitting
3. Generate embeddings via Ollama / Azure OpenAI / any OpenAI-compatible API
4. Store in Qdrant, one collection per content type — with rollback on partial failure

## MCP Tools

| Tool | What it searches |
|------|-----------------|
| `search_workitems` | Work items — bugs, user stories, tasks, features |
| `search_requirements` | Requirements — features, user stories, epics |
| `search_source_code` | Production source code |
| `search_test_code` | Test code — unit tests, integration tests, specs |
| `search_testcases` | Test cases including test steps |
| `search_documentation` | Wiki pages, repo docs, and uploaded documents |
| `search_commits` | Git commit history — messages, authors, change summaries |
| `retrieve_experience` | Recall relevant past experience. **Call at the start of every task.** |
| `remember` | Store information worth retaining across sessions. **Call proactively.** |

All search tools accept `query` (string), optional `top_k` (default 5), and optional `source_name` filter.

## Source configuration (generic by design)

Each source has a **type** (routes documents to a Qdrant collection) and a **provider** (how documents are fetched):

| Provider | Description |
|----------|-------------|
| **HTTP API** | Any JSON endpoint: GET/POST, Bearer / Basic / API-key auth (credentials by name), items dot-path, ID/title/content field mappings, next-page-URL pagination |
| **Manual** | Paste text directly |

Examples expressible as pure configuration:
- **Azure DevOps**: `https://dev.azure.com/{org}/{project}/_apis/wit/workitems?...` with Basic auth, empty username, PAT as the password credential, `ItemsPath=value`
- **Jira**: `https://you.atlassian.net/rest/api/3/search` with Basic auth, `ItemsPath=issues`
- **GitHub**: `https://api.github.com/repos/{o}/{r}/issues` with Bearer auth

| Source Type | Collection |
|-------------|------------|
| Work Items | `conduit_workitems` |
| Requirements | `conduit_requirements` |
| Test Cases | `conduit_testcases` |
| Commit History | `conduit_commits` |
| Source Code | `conduit_code` |
| Test Code | `conduit_testcode` |
| Documentation | `conduit_documentation` |

## Quick start (from source)

Prerequisites: [Go 1.24+](https://go.dev/dl/), [Docker](https://docs.docker.com/get-docker/) (for Qdrant), [Ollama](https://ollama.ai) (for local embeddings).

```bash
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit

docker run -d --name conduit-qdrant -p 6333:6333 \
  -v conduit_qdrant:/qdrant/storage qdrant/qdrant:v1.13.6   # or any local Qdrant
ollama pull nomic-embed-text-v2-moe

go run ./cmd/conduit
```

- Web UI: `http://localhost:8000`
- MCP endpoint: `http://localhost:8000/mcp`

Conduit has no built-in authentication — it's meant to run as a local
container reachable only from `localhost` or a trusted network.

Then: add credentials at `/credentials`, create a source at `/sources/create`, hit **Sync**, and point your MCP client at `/mcp`:

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://localhost:8000/mcp"
    }
  }
}
```

## Running in Docker

`docker-compose.yml` builds Conduit and starts it with Qdrant; for a prebuilt
image from Docker Hub use `docker-compose.hub.yml`:

```bash
docker compose up                          # build from source
docker compose -f docker-compose.hub.yml up   # pull michalondrejka/conduit
```

Both mount a volume at `/data` (`CONDUIT_DATA_DIR=/data`) so sources, config,
and credentials persist across restarts. Ollama must be reachable from the
container — point `EMBEDDING_BASE_URL` at `http://host.docker.internal:11434/v1`,
or use Azure OpenAI.

### Without cloning the repo

`docker-compose.hub.yml` pulls both images (Qdrant + Conduit) from Docker Hub,
so you only need that one file — no clone required:

```bash
curl -O https://raw.githubusercontent.com/MichalOndrejka/conduit/main/docker-compose.hub.yml
docker compose -f docker-compose.hub.yml up -d
```

### Sync performance: concurrency, Ollama tuning, and GPU

Embedding and preprocessing calls run concurrently now (bounded worker pools,
not one request at a time), controlled by env vars with sane defaults:

| Variable | Default | What it does |
| --- | --- | --- |
| `EMBEDDING_CONCURRENCY` | `4` | Max in-flight embed calls per sync |
| `PREPROCESSING_CONCURRENCY` | `4` | Max in-flight preprocessing/chat calls per sync |
| `OLLAMA_NUM_PARALLEL` | `4` | Requests Ollama itself will process in parallel — keep roughly in step with the concurrency vars above |
| `OLLAMA_MAX_LOADED_MODELS` | `2` | Keeps the embedding model and the preprocessing/chat model both resident, avoiding reload thrashing between sync phases |
| `OLLAMA_KEEP_ALIVE` | `30m` | Keeps a model warm between sync runs instead of unloading it right after each request |

On a host with an NVIDIA GPU and the `nvidia-container-toolkit` installed,
layer `docker-compose.gpu.yml` on top to run Ollama on the GPU:

```bash
docker compose -f docker-compose.yml -f docker-compose.gpu.yml up -d
docker compose -f docker-compose.hub.yml -f docker-compose.gpu.yml up -d   # prebuilt image
```

Compose can't detect GPU presence automatically, so CPU-only remains the
default — just omit `-f docker-compose.gpu.yml` on hosts without a GPU.

## Configuration

`config.json` (auto-created with Ollama defaults) plus environment overrides:

| Variable | Purpose |
|----------|---------|
| `PORT` | Listen port (default `8000`) |
| `QDRANT_HOST` / `QDRANT_PORT` / `QDRANT_HTTPS` / `QDRANT_API_KEY` | Qdrant connection |
| `EMBEDDING_PROVIDER` | `openai-compatible` (default) or `azure-openai` |
| `EMBEDDING_MODEL` / `EMBEDDING_BASE_URL` / `EMBEDDING_DIMENSIONS` / `EMBEDDING_MAX_INPUT_TOKENS` | Embedding settings |
| `AZURE_OPENAI_ENDPOINT` / `AZURE_OPENAI_DEPLOYMENT` / `AZURE_OPENAI_API_VERSION` / `AZURE_OPENAI_API_KEY` | Azure OpenAI embeddings |
| `CONDUIT_CONFIG` | Path to `config.json` |
| `CONDUIT_DATA_DIR` | Directory for sources, credentials, and keys. **Required in Docker.** |
| `CONDUIT_SECRET_KEY` | Base64url Fernet key for `credentials.enc.json`. Pin so credentials survive container recreation. |

## Project structure

```
cmd/conduit/        entrypoint + `conduit search` verification CLI
internal/
  config/           config.json + env overrides
  models/           domain models & constants
  secrets/          Fernet credential store
  rag/              qdrant REST client, embeddings, chunker, indexer, search, PCA
  memory/           experience store
  mcptools/         MCP tool registrations (mark3labs/mcp-go)
  sources/          generic source abstraction (API + manual)
  syncsvc/          sync orchestration (pause/cancel, status persistence)
  syncctl/          pause/cancel control + progress stores
  health/           background connectivity probes
  store/            conduit-sources.json store
  web/              routes, templates
Dockerfile          multi-stage distroless build
```

## Running tests

```bash
go test ./...
```

Includes a cross-language fixture test proving the Go Fernet store decrypts
credentials written by the original Python implementation (see
[docs/go-port.md](docs/go-port.md)), and integration tests running the full
sync pipeline against an in-memory fake Qdrant.

## Contributing

Contributions are welcome. Please open an issue to discuss a feature or bug before submitting a pull request.

## License

MIT

## Further reading

- [Go port architecture & migration](docs/go-port.md)
- [MCP tools reference](docs/mcp-tools.md) — tool signatures, search parameters, experience store
- [Source configuration reference](docs/sources.md) — HTTP API / Manual providers, field mappings, platform presets
- [Configuration reference](docs/configuration.md) — full `config.json` schema, env vars, preprocessing
