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

## GPU (optional)

Without a GPU, models run on CPU. macOS, Intel and AMD on Windows always do
(AMD on Windows: use the [standalone container](standalone.md)).

**NVIDIA, Windows:** install a current [NVIDIA driver](https://www.nvidia.com/drivers), then:

```
wsl --update
```

**NVIDIA, Linux:** install the driver and the
[NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html), then:

```
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

Check the GPU is visible:

```
docker run --rm --gpus all ubuntu nvidia-smi
```

Install with NVIDIA:

```
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.nvidia.yml up
```

**AMD, Linux:** install the ROCm driver ([supported cards](https://docs.ollama.com/gpu)), then:

```
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.amd.yml up
```

Setup fails unless the models run fully on the GPU. Keep the same `-f` list on
every `up` and `pull` below.

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

Proxy, models, data: [Customization](../customization.md).
