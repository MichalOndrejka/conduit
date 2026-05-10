# Conduit

![Python 3.12+](https://img.shields.io/badge/python-3.12%2B-blue)
![Docker](https://img.shields.io/badge/docker-michalondrejka%2Fconduit-blue?logo=docker)
![License](https://img.shields.io/badge/license-MIT-green)

A **RAG + MCP server** that gives Claude semantic search over your Azure DevOps data, custom APIs, and uploaded documents — plus a persistent **Experience** store so Claude can remember facts, preferences, and past decisions across sessions.

## Features

- **9 MCP search tools** covering work items, requirements, source code, test code, test cases, test results, builds, commits, and documentation
- **Language-aware code parsing** for C#, TypeScript, Go, PowerShell, and Markdown
- **Local embeddings via Ollama** — no API keys or external services required
- **Persistent memory** across sessions via the Experience store
- **Web UI** for managing sources, triggering syncs, and configuring settings
- **Transactional indexing** — failed syncs leave existing data intact
- **Azure DevOps support** with PAT, bearer, NTLM, Kerberos, and API-key auth — cloud and on-premise TFS/VSTS
- **Custom API and manual upload** providers for any data source

## How it works

```
Claude
  └─► MCP Tools (Conduit)
        └─► Qdrant Vector Store
              └─► Indexed from sources (ADO / Custom API / Manual)
```

**Indexing pipeline**
1. Fetch documents from configured sources
2. Parse code with language-aware parsers (C#, TypeScript, Go, PowerShell, Markdown)
3. Chunk text with sentence-boundary and newline-priority splitting
4. Generate embeddings via OpenAI / Ollama / any OpenAI-compatible API
5. Store in Qdrant, one collection per content type

**Search pipeline**
1. Claude calls an MCP search tool with a natural-language query
2. Query is embedded and semantically searched against the relevant Qdrant collection
3. Top-K results returned with similarity scores

## MCP Tools

| Tool | What it searches |
|------|-----------------|
| `search_workitems` | Work items — bugs, user stories, tasks, features |
| `search_requirements` | Requirements — features, user stories, epics |
| `search_source_code` | Production source code (classes, methods, functions) |
| `search_test_code` | Test code — unit tests, integration tests, specs |
| `search_builds` | Pipeline build results and failure details |
| `search_testcases` | Test cases including test steps |
| `search_documentation` | Wiki pages, repo docs, and uploaded documents |
| `search_test_results` | Test execution results — outcomes and error messages |
| `search_commits` | Git commit history — messages, authors, change summaries |
| `retrieve_experience` | Recall relevant past experience. **Call at the start of every task.** |
| `remember` | Store information worth retaining across sessions. **Call proactively.** |

All search tools accept `query` (string), optional `top_k` (default 5), and optional `source_name` filter.

## Source types

Each source type maps to a Qdrant collection. Within any source type you can choose the backend using the **provider tab**:

| Tab | Description |
|-----|-------------|
| **Azure DevOps** | Fetch from ADO REST APIs using PAT, bearer, NTLM, or API-key auth |
| **Custom API** | Fetch from any HTTP JSON endpoint with configurable field mapping |
| **Manual** | Paste text directly or upload a PDF, TXT, or Markdown file |

| Source Type | Collection | Azure DevOps data |
|-------------|------------|-------------------|
| Work Items | `conduit_workitems` | Bugs, tasks, user stories, features — filtered by type or custom WIQL |
| Requirements | `conduit_requirements` | Features, epics, user stories — filtered by type or custom WIQL |
| Test Cases | `conduit_testcases` | Test case definitions with steps and automation status |
| Test Results | `conduit_testresults` | Runtime test outcomes, error messages, stack traces |
| Git Commits | `conduit_commits` | Commit messages, authors, change counts |
| Source Code | `conduit_code` | Source files filtered by glob pattern |
| Test Code | `conduit_testcode` | Test files — unit tests, integration tests, specs |
| Documentation | `conduit_documentation` | Wiki pages with optional path filter; or file upload |
| Build Results | `conduit_builds` | Recent CI/CD builds and failed task details |

## Prerequisites

- Python 3.12+
- [uv](https://docs.astral.sh/uv/) (package manager)
- [Docker](https://docs.docker.com/get-docker/) (for Qdrant)
- [Ollama](https://ollama.ai) (for embeddings)

## Quick start

### 1. Clone and install

```bash
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit
uv sync
```

### 2. Start Qdrant

```bash
docker-compose up -d qdrant
```

### 3. Pull an embedding model

```bash
ollama pull nomic-embed-text-v2-moe
```

The default config points to `http://localhost:11434/v1` with this model. Change the model via the **Settings** page or by editing `config.json`.

### 4. Run Conduit

```bash
uv run uvicorn app.main:app --host 0.0.0.0 --port 5000 --reload
```

- Web UI: `http://localhost:5000`
- MCP endpoint: `http://localhost:5000/mcp`

### 5. Add sources and sync

Open `http://localhost:5000`, click **Add Source**, choose a type, select the backend tab (Azure DevOps / Custom API / Manual), fill in the connection details, and hit **Save & Sync**.

### 6. Connect to Claude

**Claude Desktop** — add to `claude_desktop_config.json`:
```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://localhost:5000/mcp"
    }
  }
}
```

**VS Code** — `.vscode/mcp.json` is already included in the repo.

## Docker (run without cloning)

Pull and run the pre-built image from Docker Hub — no Python or uv required:

```bash
docker pull michalondrejka/conduit:latest
```

Start Qdrant and Conduit together using the provided compose file:

```bash
docker-compose -f docker-compose.hub.yml up
```

Ollama must be running on the host with an embedding model pulled (`ollama pull nomic-embed-text-v2-moe`).

The web UI will be available at `http://localhost:8000`. Configure embeddings and add sources via the Settings page.

> **Credentials** — pass ADO tokens, API keys, and other secrets as environment variables. The config UI lets you reference them by name so they are never stored in the config file.

### Build & push (maintainers)

```bash
# Build
docker build -t michalondrejka/conduit:latest .

# Push
docker push michalondrejka/conduit:latest

# Versioned release
docker tag michalondrejka/conduit:latest michalondrejka/conduit:v0.1.0
docker push michalondrejka/conduit:v0.1.0
```

## Full stack (Docker Compose)

Run Qdrant and Conduit together from source:

```bash
docker-compose up
```

## Configuration reference

`config.json` (auto-created with Ollama defaults on first run):

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

All settings are editable via the **Settings** page. Changing `dimensions` or embedding model drops all Qdrant collections and marks every source for re-indexing.

### Environment variables

| Variable | Purpose |
|----------|---------|
| `QDRANT_HOST` | Override Qdrant host (default: `localhost`) |
| `QDRANT_PORT` | Override Qdrant port (default: `6333`) |
| `CONDUIT_CONFIG` | Path to `config.json` (default: `config.json` in CWD) |
| `CONDUIT_DATA_DIR` | Directory for `conduit-sources.json` and config |

ADO credentials (PAT, bearer token, passwords) are stored as **environment variable names**, not values. The actual secret is read from the environment at sync time, so `conduit-sources.json` never contains plaintext credentials.

## Project structure

```
app/
  ado/           # Azure DevOps REST client and connection model
  memory/        # Experience store (remember / retrieve_experience)
  mcp_tools/     # FastMCP tool registrations
  parsing/       # Language-aware code parsers (C#, TS, Go, PS, Markdown)
  rag/           # Embedding, chunking, vector store, indexer, search
  sources/       # Source implementations (ADO, Custom API, Manual)
  store/         # Source config persistence (JSON file)
  sync/          # Sync orchestration service
  templates/     # Jinja2 HTML templates (Tailwind CSS)
  web/           # FastAPI route handlers
  config.py      # Configuration models and loader
  container.py   # Dependency wiring
  main.py        # App entry point (FastAPI + FastMCP + lifespan)
  models.py      # Shared domain models
tests/                  # pytest test suite
docker-compose.yml      # Full stack (Qdrant + Conduit built from source)
docker-compose.hub.yml  # Full stack (Qdrant + Conduit from Docker Hub)
Dockerfile
pyproject.toml
```

## Transactional indexing

Indexing runs in two phases to prevent partial writes:

1. **Embed phase** — all document chunks are embedded in memory. A failure here writes nothing to Qdrant.
2. **Write phase** — points are upserted in batches of 100. If any batch fails, all points written by earlier batches in this sync run are deleted before the error is surfaced.

## Running tests

```bash
uv sync
uv run pytest          # all tests
uv run pytest -v       # verbose
uv run pytest -x       # stop on first failure
uv run pytest -k "ado" # filter by keyword
```

## Contributing

Contributions are welcome. Please open an issue to discuss a feature or bug before submitting a pull request.

## License

MIT

## Further reading

- [Source configuration reference](docs/sources.md) — all config keys, provider options, auth methods
- [MCP tools reference](docs/mcp-tools.md) — tool signatures, search parameters, experience store
- [Configuration reference](docs/configuration.md) — embedding providers, chunking, environment variables
