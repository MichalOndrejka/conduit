# Installation

## Docker Compose (recommended)

`docker-compose.yml` runs Conduit, Qdrant and Ollama. Services reach each
other by name, so nothing depends on Docker reaching the host's `localhost`.

```bash
URL=https://github.com/MichalOndrejka/conduit.git#main
docker compose -f $URL:docker-compose.yml up        # first run: watch setup
docker compose -f $URL:docker-compose.yml up -d     # afterwards
docker compose -p conduit logs setup                # re-read setup's verdict
docker compose -p conduit down                      # stop (keeps data)
```

Replace `#main` with a tag (e.g. `#v1.2.0`) to pin a version. The `-f` list
must be the same on every `up`, including any GPU overlay.

### Setup step

Before Conduit starts, `setup` checks Qdrant and Ollama, downloads missing
models, loads them and reports GPU/CPU placement. On `[FAIL]` it prints the
reason and a hint, and Conduit doesn't start.

### GPU

| Overlay | Works on |
| --- | --- |
| `docker-compose.nvidia.yml` | Windows (Docker Desktop / WSL2), Linux with `nvidia-container-toolkit` |
| `docker-compose.amd.yml` | Linux only (ROCm) |

- No overlay: CPU, with a warning.
- With an overlay: setup fails unless the models run fully on the GPU.
- Intel GPUs and AMD on Windows can't be passed to containers; they run on CPU.

### Corporate proxy

- `scripts/export-ca-certs.ps1` writes the Windows root CAs to `.\certs`.
- Set `CONDUIT_CERTS_DIR` to that folder (absolute path) and `HTTPS_PROXY`.
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

```bash
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

Needs Go 1.24+, a Qdrant and an Ollama:

```bash
ollama pull nomic-embed-text-v2-moe
go run ./cmd/conduit
```

## Container behavior

- Binds `0.0.0.0` in a container, `127.0.0.1` otherwise (`CONDUIT_HOST` overrides).
- `HEALTHCHECK` runs `conduit healthcheck` (Conduit itself only; `/health` shows Qdrant/Ollama).
- On stop, running syncs are cancelled and marked idle; re-run them.
