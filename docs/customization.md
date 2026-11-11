# Customization

Changes to the [Docker](install/docker.md) and [Podman](install/podman.md)
installs. Settings go in environment variables or a `.env` file next to where
you run `compose` (see [`.env.example`](../.env.example)).

## Corporate proxy

PowerShell (exports the Windows root CAs to `.\certs`):

```powershell
irm https://raw.githubusercontent.com/MichalOndrejka/conduit/main/scripts/export-ca-certs.ps1 | iex
$env:CONDUIT_CERTS_DIR = "$PWD\certs"
$env:HTTPS_PROXY = "http://proxy.example.com:8080"
```

bash (copy your CA `.pem` files into `~/conduit-certs` first):

```bash
export CONDUIT_CERTS_DIR=~/conduit-certs
export HTTPS_PROXY=http://proxy.example.com:8080
```

Then run `up` as usual. Add internal hosts (e.g. on-prem Azure DevOps) to
`NO_PROXY`.

## Models

`EMBEDDING_MODEL` and `PREPROCESSING_MODEL` set what setup downloads and
Conduit uses. Changing the embedding model drops the index on the next start;
re-sync your sources afterwards.

## Pin a version

Docker: replace `#main` in the URL with a tag, e.g. `#v1.2.0`. Podman:
`git checkout v1.2.0` in the clone.

## Data

Volumes `conduit_conduit_data`, `conduit_qdrant_data` and `conduit_ollama_data`
hold sources, credentials, the index and models. `down -v` deletes them.
Set `CONDUIT_SECRET_KEY` ([Configuration](configuration.md#credential-key)) to
keep credentials readable if the data volume is lost.

## Troubleshooting

```
docker compose -p conduit logs setup
```

`[FAIL]` lines name what went wrong; Conduit doesn't start until setup passes.

## Container behavior

- Binds `0.0.0.0` in a container, `127.0.0.1` otherwise (`CONDUIT_HOST` overrides).
- `HEALTHCHECK` runs `conduit healthcheck`; `/health` shows Qdrant and Ollama.
- On stop, running syncs are cancelled; re-run them.
