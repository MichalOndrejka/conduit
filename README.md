# Conduit

![Docker](https://img.shields.io/badge/docker-michalondrejka%2Fconduit-blue?logo=docker)
![License](https://img.shields.io/badge/license-MIT-green)

RAG + MCP server: semantic search over work items, code, commits and docs
from Azure DevOps and GitHub, plus live pipeline runs, pull requests and a
memory store for MCP clients.

## Quick start

With [Docker Desktop](https://docs.docker.com/get-docker/) and
[Ollama](https://ollama.com/download) running on your machine:

```
ollama pull nomic-embed-text-v2-moe
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up -d
```

Other ways: [standalone container](docs/install/standalone.md),
[from source](docs/install/source.md).

Open http://localhost:8000, add credentials and a
source, and **Sync**. Then point your MCP client at it:

```json
{ "mcpServers": { "conduit": { "type": "http", "url": "http://localhost:8000/mcp" } } }
```

> [!WARNING]
> Conduit has no authentication. Keep it on localhost or a trusted network.

## Documentation

- Install: [Docker](docs/install/docker.md),
  [standalone](docs/install/standalone.md), [from source](docs/install/source.md)
- [Customization](docs/customization.md): corporate proxy, models, data
- [Configuration](docs/configuration.md)
- [Sources](docs/sources.md)
- [MCP tools](docs/mcp-tools.md)
- [Dashboard](docs/stats.md)
- [Architecture & development](docs/go-port.md)

## License

MIT
