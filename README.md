# Conduit

A RAG (Retrieval-Augmented Generation) MCP server that gives Claude semantic search over your Azure DevOps repositories, work items, test cases, pipeline builds, and documents.

## How it works

Conduit indexes your data into a [Qdrant](https://qdrant.tech/) vector store using OpenAI embeddings, then exposes 6 MCP search tools that Claude can call to retrieve relevant context before answering questions.

```
Claude
  └─► MCP Search Tools (Conduit.McpServer)
        └─► Qdrant Vector Store
              └─► Indexed via: ADO Repos / Work Items / Builds / Test Cases / Docs
```

**Indexing pipeline:**
1. Fetch documents from configured sources (ADO APIs, git clone, manual upload)
2. Parse code with language-aware parsers (C#, TypeScript, Go, PowerShell, Markdown)
3. Chunk text and generate embeddings via OpenAI
4. Store in Qdrant organized by content type

**Search pipeline:**
1. Claude calls an MCP tool with a natural-language query
2. Query is embedded and semantically searched against the relevant Qdrant collection
3. Top-K results returned with similarity scores

## MCP Tools

| Tool | Description |
|------|-------------|
| `search_ado_code` | Source code from Azure DevOps git repositories |
| `search_ado_workitems` | Work items (bugs, user stories, tasks, features) |
| `search_ado_builds` | Pipeline build results and failed task logs |
| `search_ado_requirements` | Product and software requirements documents |
| `search_ado_testcases` | Test cases and test steps |
| `search_manual_documents` | Manually uploaded documents (ADRs, design docs, etc.) |

All tools accept a `query` string, optional `topK` (default 5), and optional `sourceName` filter.

## Prerequisites

- [.NET 10 SDK](https://dotnet.microsoft.com/download)
- [Docker](https://docs.docker.com/get-docker/) (for Qdrant)
- OpenAI API key (for embeddings)
- Azure DevOps Personal Access Token (for ADO sources)

## Setup

### 1. Start Qdrant

```bash
docker-compose up -d
```

### 2. Set environment variables

```bash
export OPENAI_API_KEY="sk-..."
```

### 3. Configure sources

Edit `src/Conduit.McpServer/conduit-sources.json` to define your data sources. Each source needs a `type`, `name`, and type-specific config.

**Example — ADO code repository:**
```json
{
  "Id": "...",
  "Type": "ado-code",
  "Name": "My Repo",
  "Enabled": true,
  "Config": {
    "organization": "my-org",
    "project": "my-project",
    "repository": "my-repo",
    "branch": "main",
    "pat": "...",
    "globPatterns": ["**/*.cs", "**/*.ts"]
  }
}
```

**Example — ADO work item query:**
```json
{
  "Id": "...",
  "Type": "ado-workitem-query",
  "Name": "Active Bugs",
  "Enabled": true,
  "Config": {
    "organization": "my-org",
    "project": "my-project",
    "pat": "...",
    "wiql": "SELECT [Id] FROM WorkItems WHERE [Work Item Type] = 'Bug' AND [State] <> 'Closed'"
  }
}
```

Source types: `manual`, `ado-code`, `ado-workitem-query`, `ado-pipeline-build`, `ado-requirements`, `ado-test-case`

### 4. Run the server

```bash
dotnet run --project src/Conduit.McpServer
```

The MCP server listens at `http://localhost:5000/mcp`.
A configuration UI is available at `http://localhost:5000`.

### 5. Trigger initial sync

Use the web UI at `http://localhost:5000` to sync data sources, or the sync will run on startup.

### 6. Connect to Claude

Add to your Claude MCP configuration:

```json
{
  "mcpServers": {
    "conduit": {
      "url": "http://localhost:5000/mcp"
    }
  }
}
```

## Configuration

`src/Conduit.McpServer/appsettings.json`:

```json
{
  "Qdrant": {
    "Host": "localhost",
    "GrpcPort": 6334,
    "EmbeddingDim": 1536
  },
  "OpenAI": {
    "EmbeddingModel": "text-embedding-3-small",
    "ApiKey": ""
  },
  "Chunking": {
    "MaxChunkSize": 2000,
    "Overlap": 200
  },
  "SourcesFilePath": "conduit-sources.json"
}
```

The `OpenAI:ApiKey` can be set via the `OPENAI_API_KEY` environment variable instead.

## Project structure

```
src/
  Conduit.Rag/           # Core RAG library (indexing, search, parsers, embeddings)
  Conduit.McpServer/     # ASP.NET Core MCP server + Razor Pages config UI
tests/
  Conduit.Rag.Tests/
  Conduit.McpServer.Tests/
```

## Running tests

```bash
dotnet test
```
