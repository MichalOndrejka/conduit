# MCP Tools Reference

Conduit exposes its search and memory capabilities as MCP tools that any MCP-compatible client can call.

## Connecting

The MCP endpoint is `http://localhost:8000/mcp`. For clients that use a JSON config file:

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://localhost:8000/mcp"
    }
  }
}
```

A `.vscode/mcp.json` is already included in the repo for VS Code users.

---

## Search tools

Every search tool takes one `request` argument, shaped by `mode`:

```
tool_name(request: { mode: "semantic_search", query: str, page: int, filters?: {...} })
tool_name(request: { mode: "retrieve_chunk", source_doc_id: str, chunk_index: int })
tool_name(request: { mode: "pattern_search", pattern: str, page: int, regex?: bool, filters?: {...} })
tool_name(request: { mode: "list", filters?: {...}, order_by?: str, direction?: "desc"|"asc", page?: int })
```

**Conduit is a cache.** Only syncs talk to Azure DevOps or GitHub; a search tool call reads the index and nothing else. The two exceptions are [`search_pipeline_run`](#search_pipeline_run) and [`search_pull_request`](#search_pull_request), which read the provider live and index nothing. Every response carries `as_of`, when each source in the results last synced (`"never"` if it hasn't). For a question about right now, the agent calls `source_sync` (`trigger`, then `status` until `completed`) and asks again.

**`"me"`** works in person filters (`author`, `assigned_to`). Each sync records the source token's owner (Azure DevOps `connectionData`, GitHub `/user`), and `me` matches the owners of every enabled source. Before any source has synced, `me` is an error that says so.

`semantic_search` and `pattern_search` return a page of up to 5 matches per call: `{ "results": [...], "page": N, "has_more": bool }`. `has_more: true` means call again with `page + 1` for the next batch. When `results` is empty, a `note` explains why (nothing embedded yet, no match, or the last page was already reached).

Every result carries `chunk_index`/`total_chunks`/`has_previous`/`has_next`. If a match looks cut off mid-thought, use `retrieve_chunk` with the same `source_doc_id` and `chunk_index ± 1` to fetch the adjacent text deterministically — no new search needed.

`results` holds up to 5 matches (`page` 1 is the top 5, `page` 2 the next 5, and so on):

```json
{
  "results": [
    {
      "id": "source_id_wi_12345",
      "score": 0.87,
      "text": "Work Item 12345: Fix login timeout...",
      "tags": { "source_name": "My ADO Source", "state": "Active" },
      "properties": { "title": "Fix login timeout", "web_url": "https://dev.azure.com/org/proj/_workitems/edit/12345" },
      "source_doc_id": "source_id_wi_12345",
      "chunk_index": 0,
      "total_chunks": 3,
      "has_previous": false,
      "has_next": true
    },
    { "id": "source_id_wi_12200", "score": 0.81, "text": "Work Item 12200: Session expires early...", "...": "4 more fields as above" }
  ],
  "page": 1,
  "has_more": true
}
```

### `mode: "list"` — latest, last, sorted

Answers "the latest …", "the last failed …", "every … matching …" without a search. It returns whole documents, one result each (its first chunk; walk the rest with `retrieve_chunk`), 20 per page with `has_more`, sorted by `order_by` (default: the collection's primary date, newest first). `direction: "asc"` puts the oldest or smallest first. Pages go up to 50.

| Tool | `order_by` |
|---|---|
| `search_commit` | `authored` (default) |
| others | none yet — documents come in storage order |

Conduit creates the sort indexes in the background at startup, so the first listing doesn't wait for one to build. Sort keys and filter values are computed at sync. Documents synced before Conduit stored a key don't carry it and don't appear in a listing sorted by it until their source syncs again.

```
search_commit(request={"mode":"list","filters":{"author":"me","since":"2026-11-01"}})
```

### `mode: "semantic_search"` — ranked, natural-language search

- **`query`** (required) — natural-language query, embedded and matched by similarity.
- **`page`** (required) — batch of ranked results to return, starting at `1` (most relevant).
- **`filters`** (optional) — narrow by source-system fields (`project`, `repository`, `state`, …); each tool's schema lists its own.

### `mode: "retrieve_chunk"` — deterministic fetch by ID

- **`source_doc_id`**, **`chunk_index`** (both required) — copy from a prior result, adjusting `chunk_index` by ±1 to walk to the neighboring chunk.

No embedding call is made — it's a direct point lookup, so it always returns the exact chunk (or none, with a `note`) regardless of relevance:

```json
{ "results": [ { "text": "...continuation of the previous chunk...", "source_doc_id": "source_id_wi_12345", "chunk_index": 1, "total_chunks": 3, "has_previous": true, "has_next": true } ] }
```

### `mode: "pattern_search"` — exact literal/regex match

- **`pattern`** (required) — literal substring, or (with `regex: true`) a Go RE2 regular expression.
- **`page`** (required) — batch of matches to return, starting at `1`, in storage order (not ranked).
- **`regex`** (optional, default `false`).
- **`filters`** (optional) — the same filters as `semantic_search`.

Use this instead of `semantic_search` when you need an exact match rather than a similarity ranking — e.g. finding every usage of a method or identifier. It's a client-side scan of chunk text, not a Qdrant full-text index, so `score` is always `0`. Literal patterns are case-sensitive; use `regex: true` with `(?i)` to ignore case.

One call scans up to **50,000 chunks** by default (about 90 MB of text at the default chunk size; set `search.pattern_scan_limit` on the Settings page, see [configuration.md](configuration.md#search)), reading only each chunk's text in batches of 1,000, then fetches the full results for just the matches on the requested page. Filters run inside Qdrant before the scan, so `repository` / `path` filters let it cover any collection completely. When the scan stops at the cap, the response has `"truncated": true` and a `note` — **even when the page has results**, because further matches may exist beyond the chunks scanned and `has_more: false` then doesn't mean "that's all".

## Which tool to use

When a result names another item by its ID (a work item `#1234`, a commit SHA), open it with that tool's `id` or `commit` filter. A semantic search may not surface it. Pull requests (`!45`) and pipeline runs (build IDs) are read live with `search_pull_request` and `search_pipeline_run`.

| Tool | Searches | Best for |
|---|---|---|
| `search_workitem` | Work items — bugs, tasks, user stories, features, epics, with their links and whole discussion (filter `id` opens one by its ID: `1234`, `#1234` or `AB#1234`) | Finding related issues, checking if a bug's been filed, sprint scope, following a parent or linked item |
| `search_requirement` | Requirements — features, user stories, epics | Finding relevant requirements, acceptance criteria |
| `search_source_code` | Production source code (classes, methods, functions) — no tests | Finding implementations, locating where a concept is defined |
| `search_test_code` | Test code — unit, integration, specs | Finding test coverage, patterns, and examples |
| `search_testcase` | Test case definitions, including steps | Expected behaviour, automation status |
| `search_documentation` | Wiki pages, repo docs, uploaded documents | Architectural decisions, process docs, ADRs |
| `search_commit` | Git commit history — messages, authors, file changes (filter `commit` opens one by its full or 7+ character SHA) | When a change was made, which commit introduced a behaviour |
| `search_pipeline_run` | Build pipeline runs, **live** — the latest runs, one run's failed steps and tests of every attempt, a step's log (see [below](#search_pipeline_run)) | The last (failed) build, why a run failed or needed several attempts, what a run built |
| `search_pull_request` | Pull requests, **live** — list, details, files, diffs, threads (see [below](#search_pull_request)) | Code review, what's open, who reviews what |

---

## Experience tools

### `experience_retrieve`

```
experience_retrieve(query: str, top_k: int = 5) -> str
```

Recalls relevant past experience — guidance, preferences, known mistakes, past decisions — as a JSON `experience` array of strings. **Call this at the start of every new task.**

```
experience_retrieve("implementing authentication")
→ { "experience": ["For this project, always use the NTLM auth path for on-premise TFS...", ...] }
```

### `experience_remember`

```
experience_remember(situation: str, guidance: str) -> str
```

Stores guidance for future sessions. Call proactively whenever you learn a preference, constraint, decision, or lesson the user would want enforced later.

- **`situation`** — when this applies. Be specific: "When writing C# unit tests in this repo" beats "writing tests".
- **`guidance`** — the exact instruction or fact to recall.

```
experience_remember(situation="Deploying to staging", guidance="Always run the DB migration script before deploying — missing this caused an outage in March 2024.")
→ { "status": "stored", "entry_id": "abc123..." }
```

---

## Source tools

### `source_sync`

Starts syncs and reports sync status, so an agent can refresh a source before relying on its search results.

```
source_sync(request: { mode: "status", source_ids?: [str] })
source_sync(request: { mode: "trigger", source_ids: [str] })
```

- **`status`** lists every source (or just `source_ids`) with `id`, `name`, `type`, `disabled`, `sync_status` (`idle`, `syncing`, `completed`, `failed`), `last_synced_at`, `sync_error` / `sync_error_phase` when the last sync failed, and live `progress` (`phase`, `current`, `total`) while a sync runs. IDs that match no source are listed under `not_found`. Source config is never returned.
- **`trigger`** starts the given sources syncing in the background (at most 4 at once, same as **Sync selected** in the UI) and returns immediately with one outcome per ID: `started`, `already_syncing`, `disabled`, `not_found` or `live_not_synced`. Poll `status` until `sync_status` is `completed` or `failed`.
- Pipeline Runs and Pull Requests sources are never synced: `status` reports them as `live`, and `trigger` answers `live_not_synced`. Their tools read the provider on every call.

```
source_sync(request={"mode":"trigger","source_ids":["a1b2"]})
→ { "outcomes": { "a1b2": "started" } }
```

The tool can't yet tell whether a source is out of date with its upstream system; `last_synced_at` is the only freshness signal.

### `source_manage`

Lists, reads, creates, updates and deletes API source configs, so an agent can set up a source for the user.

```
source_manage(request: { mode: "list" })
source_manage(request: { mode: "get", source_id: str })
source_manage(request: { mode: "create", name: str, type: str, config: {...}, disabled?: bool })
source_manage(request: { mode: "update", source_id: str, name?: str, type?: str, config?: {...}, disabled?: bool })
source_manage(request: { mode: "delete", source_id: str, confirm?: bool })
```

- **`list`** returns every source (`id`, `name`, `type`, `provider`, `disabled`, `sync_status`), the stored **credential names** and the source types.
- **`get`** returns one source with its `config`.
- **`create`** / **`update`** take the same config keys as the UI and the export file (see [Sources](sources.md)). `config.Provider` is `api` (Azure DevOps, the default) or `github`, and decides which keys are allowed. `update` merges `config` into the existing one; `""` removes a key. Changing `type` moves the source to another collection and removes its old vectors. Saving never syncs — call `source_sync` `trigger` afterwards (a Pipeline Runs or Pull Requests source needs no sync; `next` says so).
- **`delete`** without `confirm: true` only reports what would go (`indexed_chunks`); with it, the source and its vectors are deleted.

Credentials are referenced by name only. `Token`, `Password` and `ApiKeyValue` must name a credential from `list`; anything else is rejected (and not echoed back), so a pasted secret is never stored. New credentials are added in the UI at `/credentials`.

Sources that are currently syncing can't be changed by the tool.

```
source_manage(request={"mode":"create","name":"Bugs","type":"work-item",
  "config":{"Url":"https://ado.example/org/proj/_apis/wit/wiql?api-version=7.1","HttpMethod":"POST",
            "Body":"{\"query\":\"SELECT [System.Id] FROM WorkItems\"}","AuthType":"basic","Password":"ado-pat"}})
→ { "source": { "id": "c0ffee…", "name": "Bugs", "sync_status": "idle", "config": {...} }, "next": "Call source_sync trigger with this id to index it." }
```

---

## Pipeline run tool

### `search_pipeline_run`

Reads Azure DevOps builds and GitHub Actions workflow runs **live** from the provider. It is read-only and indexes nothing, so every answer is current. A **Pipeline Runs** source names the pipeline and the credentials (see [sources](sources.md#pipeline-runs)); it is never synced.

```
search_pipeline_run(request: { mode: "list", source_id?: str, top?: int, result?: str, status?: str, branch?: str, reason?: str, requested_for?: str })
search_pipeline_run(request: { mode: "get", source_id?: str, run_id?: int, url?: str, page?: int })
search_pipeline_run(request: { mode: "log", source_id?: str, run_id?: int, url?: str, log_id: int, page?: int })
```

`source_id` may be left out when exactly one Pipeline Runs source is enabled. `get` and `log` take either `run_id` (with that source) or `url`, the run's web link (`…/_build/results?buildId={id}` or `…/{owner}/{repo}/actions/runs/{id}`), which is matched to a configured source of the same organization or server for credentials, like a pull request URL. Every response names the source used (`source` or `credentials_from`).

- **`list`**: the latest runs, newest first: `top` of them (default the source's **Runs**, else 5; at most 100). Each has `id`, `number`, `pipeline`, `status`, `result`, `reason`, `branch`, `commit`, `requested_for`, `queued`/`started`/`finished`, `duration` and `web_url`. Filters pass to the provider as given: Azure DevOps `result` is `succeeded`, `partiallySucceeded`, `failed` or `canceled` and `status` `completed`, `inProgress`, `notStarted`…; GitHub takes its own values (`failure`, `success`, `in_progress`, …), `reason` is the event and `requested_for` the actor's login. `branch` takes `main` or a full ref.
- **`get`**: one run with `attempts`, `issues` (timeline errors and warnings, or GitHub annotations, each with its `Stage > Job > Task` path), `failed_stages`/`failed_jobs`, `failed_steps` of **every attempt** (each with `path`, `attempt`, `result`, `log_id` and `web_url`), `tests` (failed and flaky tests with outcome, attempt, job, error message, stack trace, sub-results and attachments; 20 per `page`, with `total_tests` and `tests_has_more`), `test_run_attachments`, `changes` (the commits built since the previous run) and `artifacts` (metadata only). `cached` is true when a completed run came from the in-memory cache.
- **`log`**: one failed step's complete log (Azure DevOps log id, GitHub job id, from `get`), 500 lines per page with `total_pages`, `first_line` and `last_line`. Without `page` it returns the **last** page, where the error usually is.

```
1. search_pipeline_run(request={"mode":"list","result":"failed","branch":"main","top":1})      → the last failed build on main
2. search_pipeline_run(request={"mode":"get","run_id":4711})                                  → failed_steps[0].log_id: 23
3. search_pipeline_run(request={"mode":"log","run_id":4711,"log_id":23})                      → the end of the failing step's log
```

---

## Pull request tool

### `search_pull_request`

Finds Azure DevOps and GitHub pull requests and reads them **live** from the provider, for code review and questions about changes. It is read-only and indexes nothing, so every answer is current.

```
search_pull_request(request: { mode: "list", source_id: str, status?: "active"|"completed"|"abandoned"|"all", author?: str, reviewer?: str, target_branch?: str, page?: int })
search_pull_request(request: { mode: "get", url: str })
search_pull_request(request: { mode: "files", url: str, page?: int, from_iteration?: int, to_iteration?: int })
search_pull_request(request: { mode: "diff", url: str, path?: str, page?: int, include_content?: bool, from_iteration?: int, to_iteration?: int })
search_pull_request(request: { mode: "threads", url: str, page?: int })
search_pull_request(request: { mode: "iterations", url: str })
search_pull_request(request: { mode: "commits", url: str, page?: int })
```

**Credentials.** `url` is the pull request's web link: `…/{project}/_git/{repo}/pullrequest/{id}` (Azure DevOps Services or Server) or `…/{owner}/{repo}/pull/{number}` (github.com or GitHub Enterprise Server). Conduit uses the credentials of a configured source on the same organization or server. It prefers a source of the same repository, then of the same project or owner, then any source there. Between equally close sources, an enabled one wins. Every response says which source was used in `credentials_from`. A URL that no source matches is an error, so add a source for that organization first. `list` has no URL, so it takes the `source_id` of a source of the repository instead. For Azure DevOps that must be a code, test code, documentation, commit-history or pull-request source.

- **`list`**: pull requests, newest first, 20 per page with `has_more`. `status` defaults to `active`. `author` and `reviewer` take `me` (the token's owner), an email (Azure DevOps) or a login (GitHub). On GitHub, `reviewer` matches pull requests whose review is still requested from that user.
- **`get`**: title, description, branches, merge status, head and base commits, reviewers with their votes, linked work items (on GitHub, the issues the description closes with `fixes #12`-style keywords), and checks (Azure DevOps statuses and branch policies; GitHub check runs and commit statuses). If work items or checks can't be read, `notes` says so and the rest is still returned.
- **`files`**: the changed files with change type and `additions`/`deletions`, 20 per page, plus `total_files`.
- **`diff`**: unified diffs from the merge base to head, for one `path` or a `page` of 20 files. `include_content: true` adds each file's full text at head, for the code around a change.
- **`threads`**: existing comment threads, 20 per page. Each has `kind` (`file` with `path`/`line`, `general`, or `review` for a GitHub review summary), `status` (Azure DevOps: `active`, `fixed`, …), and its comments with `reply_to`. Azure DevOps system comments (votes, pushes) are left out. GitHub's REST API doesn't report whether a thread is resolved; `outdated` marks comments on lines the latest changes replaced.
- **`iterations`**: the pull request's updates, which are pushes on Azure DevOps and commits on GitHub. `files` and `diff` accept `from_iteration` (0 = the merge base, the default) and `to_iteration` (default the latest), so `from_iteration: N` shows only what changed since iteration N.
- **`commits`**: the commits with full messages, 20 per page.

**Large pull requests** are paged, never truncated. Where GitHub itself leaves data out, the response says so in `incomplete`, either on the file or at the top level. GitHub omits patches for binary or very large files, lists at most 3000 files and 250 commits of a pull request, and compares at most 300 files between iterations. Azure DevOps diffs are built from the file contents, so every text file's diff is complete. On both providers, binary files show only their path and change type.

```
1. search_pull_request(request={"mode":"get","url":"https://dev.azure.com/acme/Shop/_git/web/pullrequest/12"})
2. search_pull_request(request={"mode":"files","url":"…/pullrequest/12"})            → total_files: 7
3. search_pull_request(request={"mode":"diff","url":"…/pullrequest/12","path":"/src/cart.ts","include_content":true})
4. search_pull_request(request={"mode":"threads","url":"…/pullrequest/12"})          → skip points already raised
```

---

## Usage patterns

**Start of every task:** `experience_retrieve` → search tools for context → do the work → `experience_remember` anything learned.

**Scoping to one repository** (e.g. two ADO repos):
```
search_source_code(request={"mode": "semantic_search", "query": "authentication middleware", "page": 1, "filters": {"repository": "backend-api"}})
```

**Following a cut-off match to its neighboring chunk:**
```
1. search_documentation(request={"mode": "semantic_search", "query": "deployment rollback steps", "page": 1})
   → chunk_index=2, total_chunks=4, has_next=true, looks cut off mid-sentence
2. search_documentation(request={"mode": "retrieve_chunk", "source_doc_id": "<from step 1>", "chunk_index": 3})
   → the next chunk, picking up where it left off
```

**Finding all usages of a method or identifier:**
```
1. search_source_code(request={"mode": "pattern_search", "pattern": "CalculateTotal", "page": 1})
2. search_source_code(request={"mode": "pattern_search", "pattern": "CalculateTotal", "page": 2})
   ... keep incrementing page until has_more is false
```
Use `search_test_code` the same way to find where a method is tested.

**Combining tools for a code change:** `experience_retrieve` → `search_source_code` (find the implementation) → `search_test_code` (find its tests) → `search_workitem` (related requirements or bugs).

**Reviewing a pull request:** `experience_retrieve` (review conventions) → `search_pull_request` `list` (by author, reviewer or target branch) if you don't have its URL → `get` → `files` → `diff` per file → `threads` → `search_source_code` / `search_test_code` for callers and tests of what changed → `search_commit` for why the changed code was written that way.
