# Install with Podman

Runs Conduit, Qdrant and Ollama from a clone (Podman can't load compose files
from a Git URL).

## Prerequisites

- [Podman](https://podman.io/docs/installation) with a compose provider
  ([Podman Desktop](https://podman-desktop.io) installs one)
- [Git](https://git-scm.com/downloads)
- 8 GB memory, 10 GB disk
- Windows: [WSL 2](https://learn.microsoft.com/windows/wsl/install) (`wsl --install`)

Windows and macOS only, once:

```
podman machine init --memory 8192
podman machine start
```

## Install

```
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit
podman compose up
```

Wait for `Setup done` (the first run downloads the models), then open
http://localhost:8000.

## Run in the background

Press Ctrl+C, then:

```
podman compose up -d
```

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

GPU, proxy, models: [Customization](../customization.md).
