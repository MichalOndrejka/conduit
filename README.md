# Conduit

A RAG + MCP server that gives Claude semantic search over your Azure DevOps data, custom API endpoints, and uploaded documents — plus a persistent **Experience** store so Claude can remember facts, preferences, and past decisions across sessions.

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
| `search_documents` | Manually uploaded documents and pasted text |
| `search_workitems` | Work items — bugs, user stories, tasks, features |
| `search_code` | Source code (classes, methods, functions) |
| `search_builds` | Pipeline build results and failure details |
| `search_testcases` | Test cases including test steps |
| `search_documentation` | Wiki pages and documentation sections |
| `search_pullrequests` | Pull requests — titles, descriptions, reviewers |
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
| Work Items | `search_workitems` | Bugs, tasks, user stories, features — filtered by type or custom WIQL |
| Test Cases | `search_testcases` | Test case definitions with steps and automation status |
| Test Results | `search_test_results` | Runtime test outcomes, error messages, stack traces |
| Pull Requests | `search_pullrequests` | PR titles, descriptions, reviewers, branch context |
| Git Commits | `search_commits` | Commit messages, authors, change counts |
| Source Code | `search_code` | Source files filtered by glob pattern |
| Documentation | `search_documentation` | Wiki pages with optional path filter; or file upload |
| Build Results | `search_builds` | Recent CI/CD builds and failed task details |

## Prerequisites

- Python 3.12+
- [uv](https://docs.astral.sh/uv/) (package manager)
- [Docker](https://docs.docker.com/get-docker/) (for Qdrant)
- OpenAI API key **or** a local Ollama instance (for embeddings)

## Quick start

### 1. Clone and install

```bash
git clone <repo-url>
cd conduit
uv sync
```

### 2. Start Qdrant

```bash
docker-compose up -d qdrant
```

### 3. Configure embeddings

Edit `config.json` (auto-created on first run) or use the **Settings** page in the web UI.

**OpenAI:**
```bash
export OPENAI_API_KEY="sk-..."
```
```json
{ "embedding": { "provider": "openai", "model": "text-embedding-3-small", "api_key_env_var": "OPENAI_API_KEY", "dimensions": 1536 } }
```

**Ollama (default):**
```bash
# No API key needed — just run Ollama locally
```
```json
{ "embedding": { "provider": "ollama", "model": "nomic-embed-text-v2-moe", "base_url": "http://localhost:11434/v1", "dimensions": 768 } }
```

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

## Docker Compose (full stack)

Run Qdrant and Conduit together:

```bash
OPENAI_API_KEY=sk-... docker-compose up
```

## Configuration reference

`config.json` (auto-created with Ollama defaults on first run):

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

All settings are editable via the **Settings** page. Changing `dimensions` or embedding model drops all Qdrant collections and marks every source for re-indexing.

### Environment variables

| Variable | Purpose |
|----------|---------|
| `OPENAI_API_KEY` | Example embedding key (referenced by name in config) |
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
tests/           # pytest test suite (227 tests)
config.json      # Runtime config (embedding, Qdrant, chunking)
conduit-sources.json  # Persisted source definitions
docker-compose.yml
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
uv run pytest          # all 227 tests
uv run pytest -v       # verbose
uv run pytest -x       # stop on first failure
uv run pytest -k "ado" # filter by keyword
```

## Further reading

- [Source configuration reference](docs/sources.md) — all config keys, provider options, auth methods
- [MCP tools reference](docs/mcp-tools.md) — tool signatures, search parameters, experience store
- [Configuration reference](docs/configuration.md) — embedding providers, chunking, environment variables
