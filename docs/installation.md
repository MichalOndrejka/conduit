# Installation

## Prerequisites

- [Docker Desktop](https://docs.docker.com/get-docker/) or
  [Podman](https://podman.io/docs/installation)
- [Git](https://git-scm.com/downloads): Compose clones the repository
- About 8 GB of memory and 10 GB of disk for images and models

**Windows:** enable virtualization in the BIOS/UEFI and
[install WSL 2](https://learn.microsoft.com/windows/wsl/install)
(`wsl --install`). Podman: `podman machine init`, then `podman machine start`.

**macOS:** give the VM at least 8 GB of memory (Docker Desktop:
Settings → Resources; Podman: `podman machine init --memory 8192`). There's no
GPU access from containers, so models run on CPU.

### GPU (optional)

**NVIDIA, Windows:**
- Install a current NVIDIA driver on Windows (nothing inside WSL), then run `wsl --update`.
- Docker Desktop needs nothing else ([guide](https://docs.docker.com/desktop/features/gpu/)).
- Podman needs the NVIDIA Container Toolkit in its machine
  ([guide](https://podman-desktop.io/docs/podman/gpu)).

**NVIDIA, Linux:** the NVIDIA driver plus the
[NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html):
`nvidia-ctk runtime configure --runtime=docker` for Docker, CDI for Podman.

Check the GPU is visible:

```bash
docker run --rm --gpus all ubuntu nvidia-smi                  # Docker
podman run --rm --device nvidia.com/gpu=all ubuntu nvidia-smi # Podman
```

**AMD, Linux:** the ROCm driver
([supported cards](https://docs.ollama.com/gpu)); `/dev/kfd` and `/dev/dri`
must exist. On SELinux hosts run `sudo setsebool container_use_devices=1`.

**AMD, Windows:** containers can't use AMD GPUs. Run
[Ollama for Windows](https://ollama.com/download) natively (Radeon RX 7000 /
PRO W7000) with the [standalone container](#standalone-container).

## Docker Compose (recommended)

`docker-compose.yml` runs Conduit, Qdrant and Ollama.

Podman can't load compose files from a Git URL: use [From a clone](#from-a-clone).

```bash
URL=https://github.com/MichalOndrejka/conduit.git#main
docker compose -f $URL:docker-compose.yml up        # first run: watch setup
docker compose -f $URL:docker-compose.yml up -d     # afterwards
docker compose -f $URL:docker-compose.yml pull      # update images
docker compose -p conduit logs setup                # re-read setup's verdict
docker compose -p conduit down                      # stop (keeps data)
```

Replace `#main` with a tag (e.g. `#v1.2.0`) to pin a version. The `-f` list
must be the same on every `up`, including any GPU overlay.

### Setup step

Before Conduit starts, `setup` checks Qdrant and Ollama, downloads missing
models, loads them and reports GPU/CPU placement. On `[FAIL]` it prints the
reason, and Conduit doesn't start.

### GPU

Add an overlay as a second `-f`:

```bash
docker compose -f $URL:docker-compose.yml -f $URL:docker-compose.nvidia.yml up
```

| Overlay | Works on |
| --- | --- |
| `docker-compose.nvidia.yml` | Windows (Docker Desktop / WSL2), Linux with `nvidia-container-toolkit` |
| `docker-compose.amd.yml` | Linux only (ROCm) |

- No overlay: CPU, with a warning.
- With an overlay: setup fails unless the models run fully on the GPU.
- Intel GPUs and AMD on Windows can't be used by containers; they run on CPU
  (see [Prerequisites](#gpu-optional) for AMD on Windows).

### Corporate proxy

```powershell
irm https://raw.githubusercontent.com/MichalOndrejka/conduit/main/scripts/export-ca-certs.ps1 | iex
$env:CONDUIT_CERTS_DIR = "$PWD\certs"
$env:HTTPS_PROXY = "http://proxy.example.com:8080"
docker compose -f https://github.com/MichalOndrejka/conduit.git#main:docker-compose.yml up
```

- The script writes the Windows root CAs to `.\certs`.
- Add internal hosts (e.g. on-prem Azure DevOps) to `NO_PROXY`.

Settings can be `$env:` variables or a `.env` file in the folder you run
`docker compose` from (see [`.env.example`](../.env.example)).

### Models

`EMBEDDING_MODEL` / `PREPROCESSING_MODEL` set what setup downloads and what
Conduit uses. Changing the embedding model drops the index on the next start
(logged as `WARNING`); re-sync your sources afterwards.

### Data

Named volumes `conduit_conduit_data`, `conduit_qdrant_data` and
`conduit_ollama_data` hold sources, credentials, the index and models.
`down -v` deletes them all. Set `CONDUIT_SECRET_KEY` (see
[Configuration](configuration.md#credential-key)) to keep credentials
readable if the data volume is lost.

### From a clone

Required for Podman (write `podman compose` instead of `docker compose`).

```bash
git clone https://github.com/MichalOndrejka/conduit.git
cd conduit
cp .env.example .env    # optional
docker compose up --build
```

`./certs` is used by default; set the GPU overlay once in `.env` with
`COMPOSE_FILE=docker-compose.yml:docker-compose.nvidia.yml` and
`COMPOSE_PATH_SEPARATOR=:`.

## Standalone container

Only Conduit, against Qdrant and Ollama you run yourself:

```bash
docker run -d --name conduit -p 127.0.0.1:8000:8000 -v conduit_data:/data \
  -e QDRANT_URL=http://host.docker.internal:6333 \
  -e EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1 \
  michalondrejka/conduit:latest
```

- Linux: add `--add-host=host.docker.internal:host-gateway`.
- Host Ollama may need `OLLAMA_HOST=0.0.0.0` to be reachable from containers.
- Internal CA: `-v ./certs:/certs:ro -e SSL_CERT_DIR=/certs`.

## From source

Needs Go 1.25+, a Qdrant and an Ollama:

```bash
ollama pull nomic-embed-text-v2-moe
go run ./cmd/conduit
```

## Container behavior

- Binds `0.0.0.0` in a container, `127.0.0.1` otherwise (`CONDUIT_HOST` overrides).
- `HEALTHCHECK` runs `conduit healthcheck` (Conduit itself only; `/health` shows Qdrant/Ollama).
- On stop, running syncs are cancelled and marked idle; re-run them.
