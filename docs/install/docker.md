# Install with Docker

Runs Conduit, Qdrant and Ollama. No clone needed.

## Prerequisites

- [Docker Desktop](https://docs.docker.com/get-docker/)
- [Git](https://git-scm.com/downloads)
- 8 GB memory, 10 GB disk
- Windows: [WSL 2](https://learn.microsoft.com/windows/wsl/install) (`wsl --install`)

## Install

```
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up
```

Wait for `Setup done` (the first run downloads the models), then open
http://localhost:8000.

## Run in the background

Press Ctrl+C, then:

```
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up -d
```

## Stop

```
docker compose -p conduit down
```

## Update

```
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml pull
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up -d
```

GPU, proxy, models: [Customization](../customization.md).
