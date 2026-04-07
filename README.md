# Conduit

A RAG + MCP server that gives Claude semantic search over your Azure DevOps repositories, work items, wikis, pipeline builds, test cases, documents, and web pages — plus a persistent **Experience** store so the LLM can remember facts, preferences, and past decisions across sessions.

## How it works

```
Claude
  └─► MCP Tools (Conduit)
        └─► Qdrant Vector Store
              └─► Indexed from: ADO Repos / Work Items / Wikis / Builds /
                                Test Cases / Requirements / HTTP Pages / Docs
```

**Indexing pipeline**
1. Fetch documents from configured sources (ADO REST APIs, git content, manual upload, HTTP pages)
2. Parse code with language-aware parsers (C#, TypeScript, Go, PowerShell, Markdown)
3. Chunk text with sentence-boundary and newline-priority splitting
4. Generate embeddings via OpenAI / Ollama / any OpenAI-compatible API
5. Store in Qdrant, one collection per content type

**Search pipeline**
1. Claude calls an MCP search tool with a natural-language query
2. Query is embedded and semantically searched against the relevant Qdrant collection
3. Top-K results returned with similarity scores

## MCP Tools

| Tool | Description |
|------|-------------|
| `search_manual_documents` | Manually uploaded documents (ADRs, design docs, PDFs, markdown) |
| `search_ado_workitems` | Work items — bugs, user stories, tasks, features |
| `search_ado_code` | Source code from Azure DevOps git repositories |
| `search_ado_builds` | Pipeline build results and failure details |
| `search_ado_requirements` | Requirements work items |
| `search_ado_testcases` | Test cases including test steps |
| `search_ado_wiki` | Azure DevOps wiki pages |
| `search_http_pages` | Indexed HTTP pages and JSON endpoints |
| `retrieve_experience` | Recall relevant past experience — preferences, mistakes, decisions. **Call at the start of every task.** |
| `remember` | Store any information worth retaining across sessions. **Call proactively.** |

All search tools accept `query` (string), optional `top_k` (default 5), and optional `source_name` filter.

## Source types

| Type | What it indexes |
|------|----------------|
| `manual` | Pasted text or uploaded file (txt, md, PDF) |
| `ado-workitem-query` | Work items returned by a WIQL query |
| `ado-code` | Source files from an ADO git repo (glob-filtered) |
| `ado-pipeline-build` | Last N builds of a pipeline definition |
| `ado-requirements` | Requirements work items via WIQL |
| `ado-test-case` | Test cases via WIQL |
| `ado-wiki` | Pages from an ADO wiki (optional path filter) |
| `http-page` | A single web page or JSON endpoint |

## Prerequisites

- Python 3.12+
- [uv](https://docs.astral.sh/uv/) (package manager)
- [Docker](https://docs.docker.com/get-docker/) (for Qdrant)
- OpenAI API key **or** a local Ollama instance (for embeddings)
- Azure DevOps Personal Access Token (for ADO sources)

## Quick start

### 1. Clone and install

```bash
git clone <repo-url>
cd conduit
uv sync
```

### 2. Start Qdrant

```bash
docker-compose up -d
```

### 3. Set environment variables

```bash
export OPENAI_API_KEY="sk-..."
# For ADO sources, set the env var you reference in the source config:
export TFS_PAT="..."
```

### 4. Run Conduit

```bash
uv run uvicorn app.main:app --host 0.0.0.0 --port 5000 --reload
```

- Web UI: `http://localhost:5000`
- MCP endpoint: `http://localhost:5000/mcp`

### 5. Add sources and sync

Open `http://localhost:5000`, click **Add Source**, choose a type, fill in the connection details, and hit **Save & Sync**. Sources can be re-synced at any time from the main page.

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

**VS Code** — `.vscode/mcp.json` is already included:
```json
{
  "servers": {
    "conduit": {
      "type": "http",
      "url": "http://localhost:5000/mcp"
    }
  }
}
```

## Docker Compose (full stack)

Run Qdrant + Conduit together:

```bash
OPENAI_API_KEY=sk-... docker-compose up
```

The `docker-compose.yml` at the repo root starts both services. Conduit waits for Qdrant to be healthy before starting.

## Configuration

`config.json` (auto-created on first run):

```json
{
  "embedding": {
    "provider": "openai",
    "model": "text-embedding-3-small",
    "api_key_env_var": "OPENAI_API_KEY",
    "base_url": "",
    "dimensions": 1536,
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

All settings are also editable via the **Settings** page in the web UI. Changing the embedding model or dimensions drops all Qdrant collections and marks sources for re-indexing.

### Alternative embedding providers

**Ollama (local):**
```json
{
  "embedding": {
    "provider": "ollama",
    "model": "nomic-embed-text",
    "base_url": "http://localhost:11434/v1",
    "dimensions": 768
  }
}
```

**Any OpenAI-compatible API:**
```json
{
  "embedding": {
    "provider": "openai-compatible",
    "model": "your-model",
    "base_url": "https://your-endpoint/v1",
    "api_key_env_var": "YOUR_API_KEY_ENV_VAR",
    "dimensions": 1536
  }
}
```

### Environment variables

| Variable | Purpose |
|----------|---------|
| `OPENAI_API_KEY` | Default API key env var (referenced by name in config) |
| `QDRANT_HOST` | Override Qdrant host (default: `localhost`) |
| `QDRANT_PORT` | Override Qdrant port (default: `6333`) |
| `CONDUIT_CONFIG` | Path to `config.json` (default: `config.json` in CWD) |
| `CONDUIT_DATA_DIR` | Directory for `conduit-sources.json` and config (optional) |

## Project structure

```
app/
  ado/           # Azure DevOps REST client
  memory/        # Experience store (remember / retrieve)
  mcp_tools/     # FastMCP tool registrations
  parsing/       # Language-aware code parsers (C#, TS, Go, PS, Markdown)
  rag/           # Embedding, chunking, vector store, indexer, search
  sources/       # Source type implementations
  store/         # Source config persistence and sync progress tracking
  sync/          # Sync orchestration service
  templates/     # Jinja2 HTML templates (Tailwind CSS dark UI)
  web/           # FastAPI routes
  config.py      # Configuration models
  container.py   # Dependency wiring
  main.py        # App entry point (FastAPI + FastMCP + lifespan)
  models.py      # Shared domain models
config.json         # Runtime config (embedding, Qdrant, chunking)
conduit-sources.json  # Persisted source definitions
docker-compose.yml
Dockerfile
pyproject.toml
```

## Transactional indexing

Indexing is split into two phases:

1. **Embed phase** — all chunks are embedded in memory. If any embedding call fails, no data has been written to Qdrant.
2. **Write phase** — points are upserted in batches of 100. If a batch fails, all points written in earlier batches of this sync run are deleted before the error propagates.

This ensures a failed sync never leaves partial or stale data in the vector store.

## Authentication for ADO sources

Conduit supports multiple auth methods per source:

| Method | When to use |
|--------|------------|
| PAT (Personal Access Token) | Azure DevOps cloud or on-premise |
| Bearer token | Generic bearer auth |
| NTLM / Negotiate | On-premise TFS with Windows auth |
| API Key header | Custom key/value header |

Credentials are stored as **environment variable names** (not values) in `conduit-sources.json`. The actual secrets are read from the environment at sync time.
