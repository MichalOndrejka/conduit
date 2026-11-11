# Run from source

For development.

## Prerequisites

- [Go 1.25+](https://go.dev/dl/)
- [Git](https://git-scm.com/downloads)
- [Ollama](https://ollama.com/download)
- [Docker Desktop](https://docs.docker.com/get-docker/) (for Qdrant)

## Install

```
ollama pull nomic-embed-text-v2-moe
docker run -d --name qdrant --restart unless-stopped -p 127.0.0.1:6333:6333 -v qdrant_data:/qdrant/storage qdrant/qdrant:v1.13.6
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit
go run ./cmd/conduit
```

Open http://localhost:8000.

Settings: [Configuration](../configuration.md). Architecture and tests:
[go-port.md](../go-port.md).
