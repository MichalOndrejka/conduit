# Go port — status and architecture

Conduit was rewritten from Python (FastAPI) to Go for a dramatically smaller
deploy footprint: a ~25 MB distroless image idling at ~20–50 MB RSS, versus
the ~1–2 GB Python image idling at 200–400 MB. Request latency is unchanged
— the workload is network-bound on the embedding API and Qdrant. The Python
implementation was removed from the repo on 2026-06-16 once the Go rewrite
was validated; this doc keeps the migration history for context.

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
internal/web/           routes, templates
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

## Compatibility with the Python app (historical)

The Python/FastAPI implementation was removed from this repo on 2026-06-16
(the Go rewrite is feature-complete and validated). While it still existed
alongside the Go code, the Go binary was a **drop-in sidegrade** against the
same on-disk state — a design constraint that still shapes the current code:

- **Credentials**: reads/writes the same Fernet-encrypted `credentials.enc.json`
  format, with the same key sources (`CONDUIT_SECRET_KEY` or `.secret_key`).
  Verified by a cross-language test fixture in `internal/secrets/store_test.go`
  (data originally written by the Python app).
- **Qdrant**: talks the same REST API/port the Python `qdrant-client` used, so
  existing collections work unchanged. (The official Go client is gRPC-only
  on port 6334 and would have required infra changes — deliberately avoided.)
- **Sources**: reads the same `conduit-sources.json` format.
- **Env vars**: same `EMBEDDING_*`, `CONDUIT_*` variables.

## Ported

- [x] Phase 1 — core search slice (config, models, secrets, qdrant, embedding, chunker, search)
- [x] Phase 2 — MCP server: 9 search tools + retrieve_experience/remember at `/mcp`
- [x] Phase 3 — web UI (sources, items browse, experience, health/status)
- [x] Phase 4 — generic sync engine + sources (API + manual providers, source/credential
      CRUD UI, sync/pause/resume/cancel with live progress, export)
- [x] Phase 5 — PCA vector map (hand-rolled power iteration, zero deps),
      background health probes with exponential backoff

The Go binary is feature-complete. Existing ADO sources synced by the old
Python era's multi-step providers (WIQL, git zip downloads) keep their
already-indexed data in Qdrant (search/MCP work unchanged); re-syncing them
requires reconfiguring as generic API sources, since the Python app that
could sync them is no longer in the repo.

## Build & run

```bash
go build ./cmd/conduit          # local binary
go test ./...                   # unit tests (incl. Fernet cross-compat)
./conduit                       # serve on :8000
./conduit search conduit_workitems "login bug"   # verification CLI
```

Conduit runs as a local container with no built-in authentication — expose it
only on localhost or a trusted network.

## Known losses (accepted in the plan)

- UMAP map view — PCA-only (no Go UMAP implementation).
