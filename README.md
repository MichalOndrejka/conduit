# Conduit

![Docker](https://img.shields.io/badge/docker-michalondrejka%2Fconduit-blue?logo=docker)
![License](https://img.shields.io/badge/license-MIT-green)

RAG + MCP server: semantic search over work items, code and docs from any
JSON-over-HTTP API, plus a persistent memory store for MCP clients.

## Install

Requires [Docker](https://docs.docker.com/get-docker/). No clone needed:

```bash
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up
```

Wait for `Setup done` (first run downloads the models), then open
http://localhost:8000. Press Ctrl+C and re-run with `up -d` to keep it
running in the background.

**NVIDIA GPU** — add a second file:

```bash
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml \
               -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.nvidia.yml up
```

AMD GPUs work on Linux only (`docker-compose.amd.yml`); other setups run on
CPU — see [GPU](docs/installation.md#gpu).

**Corporate proxy** (PowerShell):

```powershell
irm https://raw.githubusercontent.com/MichalOndrejka/conduit/main/scripts/export-ca-certs.ps1 | iex
$env:CONDUIT_CERTS_DIR = "$PWD\certs"
$env:HTTPS_PROXY = "http://proxy.example.com:8080"
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up
```

## Use

1. Add credentials at `/credentials` and a source at `/sources/create`, then **Sync**.
2. Point your MCP client at `http://localhost:8000/mcp`:

```json
{ "mcpServers": { "conduit": { "type": "http", "url": "http://localhost:8000/mcp" } } }
```

Conduit has no authentication — keep it on localhost or a trusted network.

## Docs

- [Installation](docs/installation.md) — GPU, proxy, models, data, other setups
- [Configuration](docs/configuration.md) — `config.json` and env vars
- [Sources](docs/sources.md) — connecting APIs
- [MCP tools](docs/mcp-tools.md)
- [Architecture & development](docs/go-port.md)

## License

MIT
