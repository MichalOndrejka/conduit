# Conduit

![Go 1.24+](https://img.shields.io/badge/go-1.24%2B-00ADD8?logo=go)
![Docker](https://img.shields.io/badge/docker-michalondrejka%2Fconduit-blue?logo=docker)
![License](https://img.shields.io/badge/license-MIT-green)

A **RAG + MCP server** that gives any MCP client semantic search over your data — work items, code, docs, test results from **any JSON-over-HTTP API** — plus a persistent **Experience** store for remembering facts, preferences, and past decisions across sessions.

Conduit is a single ~13 MB Go binary (≈25 MB distroless image) backed by Qdrant and any OpenAI-compatible embedding endpoint.

## Features

- **9 MCP search tools** covering work items, requirements, source code, test code, test cases, test results, builds, commits, and documentation
- **Provider-agnostic sources** — connect any JSON API through configuration (URL, auth, field mappings, pagination); no provider-specific code. Azure DevOps, Jira, GitHub, internal tools — all just configuration
- **Persistent memory** across sessions via the Experience store (`remember` / `retrieve_experience`)
- **Web UI** for managing sources, credentials, triggering syncs, and a PCA vector map
- **Account login** (n8n-style) — bcrypt-hashed owner account, JWT session cookie; Bearer API key for headless MCP clients
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
| `search_builds` | Pipeline build results and failure details |
| `search_testcases` | Test cases including test steps |
| `search_documentation` | Wiki pages, repo docs, and uploaded documents |
| `search_test_results` | Test execution results — outcomes and error messages |
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
| Test Results | `conduit_testresults` |
| Git Commits | `conduit_commits` |
| Source Code | `conduit_code` |
| Test Code | `conduit_testcode` |
| Documentation | `conduit_documentation` |
| Build Results | `conduit_builds` |

## Quick start (from source)

Prerequisites: [Go 1.24+](https://go.dev/dl/), [Docker](https://docs.docker.com/get-docker/) (for Qdrant), [Ollama](https://ollama.ai) (for local embeddings).

```bash
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit

docker compose -f docker-compose.go.yml up -d qdrant   # or any local Qdrant
ollama pull nomic-embed-text-v2-moe

go run ./cmd/conduit
```

- Web UI: `http://localhost:8000` — first visit opens the **owner setup** page
- MCP endpoint: `http://localhost:8000/mcp` (authenticate with `Authorization: Bearer <CONDUIT_API_KEY>`)

For local plain-HTTP runs set `CONDUIT_SECURE_COOKIE=false` (the session cookie is `Secure` by default).

Then: add credentials at `/credentials`, create a source at `/sources/create`, hit **Sync**, and point your MCP client at `/mcp`:

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://localhost:8000/mcp",
      "headers": { "Authorization": "Bearer <your CONDUIT_API_KEY>" }
    }
  }
}
```

## Demo deployment (Caddy TLS → Conduit → Qdrant)

Separate containers on one small VM; HTTPS via automatic Let's Encrypt:

```bash
DOMAIN=conduit.example.com \
CONDUIT_OWNER_EMAIL=you@example.com CONDUIT_OWNER_PASSWORD=... \
AZURE_OPENAI_ENDPOINT=... AZURE_OPENAI_DEPLOYMENT=... AZURE_OPENAI_API_KEY=... \
docker compose -f docker-compose.go.yml up -d
```

See [docs/go-port.md](docs/go-port.md) for the architecture and [Caddyfile](Caddyfile) for the proxy config.

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
| `CONDUIT_SECRET_KEY` | Base64url Fernet key for `credentials.enc.json`. Pin in production. |
| `CONDUIT_OWNER_EMAIL` / `CONDUIT_OWNER_PASSWORD` | Seed the owner account (otherwise first-run `/setup`) |
| `CONDUIT_JWT_SECRET` | Session-signing secret (auto-generated to `.jwt_secret` if unset) |
| `CONDUIT_SECURE_COOKIE` | Set `false` only for plain-HTTP local runs |
| `CONDUIT_API_KEY` | Bearer key for headless MCP/API clients |

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
  web/              routes, templates, n8n-style auth
Dockerfile.golang        multi-stage distroless build
docker-compose.go.yml    demo stack (Caddy + Conduit + Qdrant)
```

## Running tests

```bash
go test ./...
```

Includes a cross-language fixture test proving the Go Fernet store decrypts
credentials written by the legacy Python app, and integration tests running
the full sync pipeline against an in-memory fake Qdrant.

## Legacy Python implementation

The original Python/FastAPI implementation lives in `app/` (run with
`uv run uvicorn app.main:app`, tests with `uv run pytest`). It remains in the
repo until the Go demo is validated against production data, and it is the
only tool that can sync the legacy multi-step Azure DevOps source types
(WIQL queries, git zip downloads with code parsing). The Go backend reads the
same Qdrant collections, `conduit-sources.json`, and `credentials.enc.json`,
so both can run against the same data. See [docs/go-port.md](docs/go-port.md)
for the migration details; legacy docs: [sources](docs/sources.md),
[configuration](docs/configuration.md), [Azure deployment](docs/deployment-azure.md).

## Contributing

Contributions are welcome. Please open an issue to discuss a feature or bug before submitting a pull request.

## License

MIT

## Further reading

- [Go port architecture & migration](docs/go-port.md)
- [MCP tools reference](docs/mcp-tools.md) — tool signatures, search parameters, experience store
