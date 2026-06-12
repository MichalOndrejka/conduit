# Go port — status and architecture

Conduit is being rewritten from Python (FastAPI) to Go for a dramatically
smaller deploy footprint: a ~25 MB distroless image idling at ~20–50 MB RSS,
versus the ~1–2 GB Python image idling at 200–400 MB. Request latency is
unchanged — the workload is network-bound on the embedding API and Qdrant.

## Layout

```
cmd/conduit/            entrypoint + `conduit search` verification CLI
internal/config/        config.json + env overrides   (port of app/config.py)
internal/models/        domain models & constants     (port of app/models.py)
internal/secrets/       Fernet credential store       (port of app/store/secrets_store.py)
internal/rag/           qdrant REST client, embeddings, chunker, indexer, search, PCA
internal/memory/        experience store              (port of app/memory/service.py)
internal/mcptools/      MCP tools via mark3labs/mcp-go (port of app/mcp_tools/tools.py)
internal/sources/       generic source abstraction (API + manual — no provider code)
internal/syncsvc/       sync orchestration (fetch → index, pause/cancel)
internal/syncctl/       pause/cancel control + progress stores
internal/health/        background probes with exponential backoff
internal/store/         conduit-sources.json store
internal/web/           routes, templates, n8n-style auth
```

## Generic sources (deliberate redesign)

The Python app shipped nine Azure-DevOps-specific source providers. The Go
backend intentionally has **zero provider-specific code**: any JSON-over-HTTP
system is configured through the generic API source —

- endpoint (URL, GET/POST, raw body, extra headers)
- auth: none / Bearer / Basic / API-key header, secrets referenced by
  credential name (ADO PATs = Basic with empty username)
- response mapping: items dot-path, ID/title/content fields
- pagination via a next-page-URL dot-path, `Top` item cap
- plus the manual document provider for pasted content

ADO, Jira, GitHub, etc. become source *configurations*, not source *code*.
Constraint accepted: multi-step fetch flows (e.g. ADO WIQL → batch fetch, git
zip downloads with code parsing) are not expressible — single-endpoint JSON
APIs only.

## Compatibility with the Python app

The Go binary is a **drop-in sidegrade** against existing state:

- **Credentials**: reads/writes the same Fernet-encrypted `credentials.enc.json`
  with the same key sources (`CONDUIT_SECRET_KEY` or `.secret_key`). Verified by
  a cross-language test fixture in `internal/secrets/store_test.go`.
- **Qdrant**: talks the same REST API/port the Python `qdrant-client` used, so
  `QDRANT_HOST/PORT/HTTPS/API_KEY` and existing collections work unchanged.
  (The official Go client is gRPC-only on port 6334 and would have required
  infra changes — deliberately avoided.)
- **Sources**: reads the same `conduit-sources.json`.
- **Env vars**: same `EMBEDDING_*`, `AZURE_OPENAI_*`, `CONDUIT_*` variables.

You can pre-seed Qdrant with the Python app and point the Go binary at the
same volume.

## Auth (new, n8n-style)

- Owner account: `CONDUIT_OWNER_EMAIL`/`CONDUIT_OWNER_PASSWORD` env seed, or
  first-run `/setup` page (persists bcrypt hash to `owner.json` in the data dir).
- Session: JWT in the `conduit-auth` HttpOnly cookie (7-day sliding expiry),
  signed with `CONDUIT_JWT_SECRET` (auto-generated to `.jwt_secret` if unset).
- Headless clients (MCP): `Authorization: Bearer <CONDUIT_API_KEY>` still works.
- `/login` is rate-limited (5 attempts, refill 1/30 s per IP).
- Set `CONDUIT_SECURE_COOKIE=false` only for plain-HTTP local runs.

## Ported

- [x] Phase 1 — core search slice (config, models, secrets, qdrant, embedding, chunker, search)
- [x] Phase 2 — MCP server: 9 search tools + retrieve_experience/remember at `/mcp`
- [x] Phase 3 — auth + web UI (login/setup, sources, items browse, experience, health/status)
- [x] Phase 4 — generic sync engine + sources (API + manual providers, source/credential
      CRUD UI, sync/pause/resume/cancel with live progress, export)
- [x] Phase 5 — PCA vector map (hand-rolled power iteration, zero deps),
      background health probes with exponential backoff

The Go binary is feature-complete for the demo. Existing ADO sources from the
Python era keep their data in Qdrant (search/MCP work unchanged); re-syncing
them requires reconfiguring as generic API sources or running the Python app.

## Build & run

```bash
go build ./cmd/conduit          # local binary
go test ./...                   # unit tests (incl. Fernet cross-compat)
./conduit                       # serve on :8000
./conduit search conduit_workitems "login bug"   # verification CLI

# Demo deployment (Caddy TLS → Go app → Qdrant):
DOMAIN=demo.example.com docker compose -f docker-compose.go.yml up -d
```

## Known losses (accepted in the plan)

- UMAP map view — PCA-only when Phase 5 lands (no Go UMAP implementation).
- LLM summarization preprocessor — disabled for the demo.
