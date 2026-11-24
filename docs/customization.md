# Customization

Changes to the [Docker](install/docker.md) install. Settings go in
environment variables or a `.env` file next to where you run `compose` (see
[`.env.example`](../.env.example)).

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

These cover Conduit's own calls (source syncs). Model downloads are made by
the host's Ollama, so give it the proxy too: set `HTTPS_PROXY` as a user
environment variable (Windows: System Properties → Environment Variables;
Linux: the Ollama service's environment) and restart Ollama. If the proxy
intercepts TLS, the host must trust its CA, which a company-managed machine
usually already does.

## Models

Ollama's URL and the embedding and preprocessing models are set on the
**Settings** page. The first-run default is the host's Ollama
(`http://host.docker.internal:11434/v1`). Changing the embedding model drops
the index; re-sync your sources afterwards.

Pull a model into Ollama before selecting it: `ollama pull <model>`.

Ollama's own tuning is set where Ollama runs (environment variables, then
restart Ollama): `OLLAMA_NUM_PARALLEL` (keep in step with
`EMBEDDING_CONCURRENCY` / `PREPROCESSING_CONCURRENCY`, default 4 each),
`OLLAMA_MAX_LOADED_MODELS=2` to keep both models resident during a sync, and
`OLLAMA_KEEP_ALIVE=30m` to keep them warm between syncs.

## Pin a version

Replace `#main` in the compose URL with a tag, e.g. `#v1.2.0`.

## Data

Volumes `conduit_conduit_data` and `conduit_qdrant_data` hold sources,
credentials and the index; models live in the host's Ollama. `down -v` deletes them.
Set `CONDUIT_SECRET_KEY` ([Configuration](configuration.md#credential-key)) to
keep credentials readable if the data volume is lost.

## Troubleshooting

```
docker compose -p conduit logs conduit
```

The Settings page's Verify buttons check the connection to Ollama and
whether the model is available.

## Container behavior

- Binds `0.0.0.0` in a container, `127.0.0.1` otherwise (`CONDUIT_HOST` overrides).
- `HEALTHCHECK` runs `conduit healthcheck`; `/health` shows Qdrant and Ollama.
- On stop, running syncs are cancelled; re-run them.
