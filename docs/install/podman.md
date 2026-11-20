# Install with Podman

Runs Conduit and Qdrant from a clone, against the Ollama on your machine
(Podman can't load compose files from a Git URL).

## Prerequisites

- [Podman](https://podman.io/docs/installation) with a compose provider
  ([Podman Desktop](https://podman-desktop.io) installs one)
- [Git](https://git-scm.com/downloads)
- [Ollama](https://ollama.com/download), running on this machine. Conduit
  reaches it at `localhost:11434` (from containers, `host.docker.internal`).
  Linux: start it with `OLLAMA_HOST=0.0.0.0` so containers can reach it.
- 8 GB memory, 10 GB disk
- Windows: [WSL 2](https://learn.microsoft.com/windows/wsl/install) (`wsl --install`)

Windows and macOS only, once:

```
podman machine init --memory 8192
podman machine start
```

## Install

Pull the embedding model into Ollama:

```
ollama pull nomic-embed-text-v2-moe
```

Then start Conduit:

```
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit
podman compose up -d
```

Open http://localhost:8000. Ollama's URL and models are on the Settings page.

## GPU (optional)

Ollama runs on the host, so it uses any GPU it supports (NVIDIA, AMD
including on Windows, Apple) with no container setup. Install the GPU
driver; `ollama ps` shows where a loaded model runs (`100% GPU` in the
`PROCESSOR` column). Without a GPU, models run on CPU.

## Stop

```
podman compose down
```

## Update

```
git pull
podman compose pull
podman compose up -d
```

Proxy, models, data: [Customization](../customization.md).
