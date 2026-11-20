# Install with Docker

Runs Conduit and Qdrant against the Ollama on your machine. No clone needed.

## Prerequisites

- [Docker Desktop](https://docs.docker.com/get-docker/)
- [Git](https://git-scm.com/downloads)
- [Ollama](https://ollama.com/download), running on this machine. Conduit
  reaches it at `localhost:11434` (from containers, `host.docker.internal`).
  Linux: start it with `OLLAMA_HOST=0.0.0.0` so containers can reach it.
- 8 GB memory, 10 GB disk
- Windows: [WSL 2](https://learn.microsoft.com/windows/wsl/install) (`wsl --install`)

## Install

Pull the embedding model into Ollama:

```
ollama pull nomic-embed-text-v2-moe
```

Then start Conduit:

```
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up -d
```

Open http://localhost:8000. Ollama's URL and models are on the Settings page.

## GPU (optional)

Ollama runs on the host, so it uses any GPU it supports (NVIDIA, AMD
including on Windows, Apple) with no container setup. Install the GPU
driver; `ollama ps` shows where a loaded model runs (`100% GPU` in the
`PROCESSOR` column). Without a GPU, models run on CPU.

## Stop

```
docker compose -p conduit down
```

## Update

```
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml pull
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up -d
```

Proxy, models, data: [Customization](../customization.md).
