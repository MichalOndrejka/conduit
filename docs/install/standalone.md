# Install as a standalone container

Runs Conduit and Qdrant in containers, against Ollama installed on the host.
Use it when Ollama must run natively, e.g. an AMD GPU on Windows.

## Prerequisites

- [Docker Desktop](https://docs.docker.com/get-docker/)
- [Ollama](https://ollama.com/download)
- 8 GB memory, 10 GB disk
- Windows: [WSL 2](https://learn.microsoft.com/windows/wsl/install) (`wsl --install`)
- Linux: start Ollama with `OLLAMA_HOST=0.0.0.0` so containers can reach it

## Install

```
ollama pull nomic-embed-text-v2-moe
docker network create conduit
docker run -d --name qdrant --network conduit --restart unless-stopped -v qdrant_data:/qdrant/storage qdrant/qdrant:v1.13.6
```

PowerShell:

```powershell
docker run -d --name conduit --network conduit --restart unless-stopped `
  -p 127.0.0.1:8000:8000 -v conduit_data:/data `
  --add-host=host.docker.internal:host-gateway `
  -e QDRANT_URL=http://qdrant:6333 `
  -e EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1 `
  michalondrejka/conduit:latest
```

bash:

```bash
docker run -d --name conduit --network conduit --restart unless-stopped \
  -p 127.0.0.1:8000:8000 -v conduit_data:/data \
  --add-host=host.docker.internal:host-gateway \
  -e QDRANT_URL=http://qdrant:6333 \
  -e EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1 \
  michalondrejka/conduit:latest
```

Open http://localhost:8000.

## Stop

```
docker stop conduit qdrant
```

## Update

```
docker pull michalondrejka/conduit:latest
docker rm -f conduit
```

Then run the `docker run -d --name conduit …` command again. Data stays in the
`conduit_data` volume.

Proxy, models: [Customization](../customization.md).
