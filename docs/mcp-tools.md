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
tool_name(request: { mode: "semantic_search", query: str, page: int, source_name?: str })
tool_name(request: { mode: "retrieve_chunk", source_doc_id: str, chunk_index: int })
tool_name(request: { mode: "pattern_search", pattern: str, page: int, regex?: bool, source_name?: str })
```

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

### `mode: "semantic_search"` — ranked, natural-language search

- **`query`** (required) — natural-language query, embedded and matched by similarity.
- **`page`** (required) — batch of ranked results to return, starting at `1` (most relevant).
- **`source_name`** (optional) — restrict results to one source.

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
- **`source_name`** (optional) — restrict results to one source.

Use this instead of `semantic_search` when you need an exact match rather than a similarity ranking — e.g. finding every usage of a method or identifier. It's a client-side scan of chunk text, not a Qdrant full-text index, so `score` is always `0`.

## Which tool to use

| Tool | Searches | Best for |
|---|---|---|
| `search_workitem` | Work items — bugs, tasks, user stories, features, epics | Finding related issues, checking if a bug's been filed, sprint scope |
| `search_requirement` | Requirements — features, user stories, epics | Finding relevant requirements, acceptance criteria |
| `search_source_code` | Production source code (classes, methods, functions) — no tests | Finding implementations, locating where a concept is defined |
| `search_test_code` | Test code — unit, integration, specs | Finding test coverage, patterns, and examples |
| `search_testcase` | Test case definitions, including steps | Expected behaviour, automation status |
| `search_documentation` | Wiki pages, repo docs, uploaded documents | Architectural decisions, process docs, ADRs |
| `search_commit` | Git commit history — messages, authors, file changes | When a change was made, which commit introduced a behaviour |
| `search_pipeline_run` | Build pipeline runs — run summaries, failed-step logs, failed tests (filters: `result`, `branch`) | Why a run failed, whether an error or test failed before, which commit a run built |

---

## Experience tools

### `retrieve_experience`

```
retrieve_experience(query: str, top_k: int = 5) -> str
```

Recalls relevant past experience — guidance, preferences, known mistakes, past decisions — as a JSON `experience` array of strings. **Call this at the start of every new task.**

```
retrieve_experience("implementing authentication")
→ { "experience": ["For this project, always use the NTLM auth path for on-premise TFS...", ...] }
```

### `remember`

```
remember(situation: str, guidance: str) -> str
```

Stores guidance for future sessions. Call proactively whenever you learn a preference, constraint, decision, or lesson the user would want enforced later.

- **`situation`** — when this applies. Be specific: "When writing C# unit tests in this repo" beats "writing tests".
- **`guidance`** — the exact instruction or fact to recall.

```
remember(situation="Deploying to staging", guidance="Always run the DB migration script before deploying — missing this caused an outage in March 2024.")
→ { "status": "stored", "entry_id": "abc123..." }
```

---

## Source tools

### `sync_sources`

Starts syncs and reports sync status, so an agent can refresh a source before relying on its search results.

```
sync_sources(request: { mode: "status", source_ids?: [str] })
sync_sources(request: { mode: "trigger", source_ids: [str] })
```

- **`status`** lists every source (or just `source_ids`) with `id`, `name`, `type`, `disabled`, `sync_status` (`idle`, `syncing`, `completed`, `failed`), `last_synced_at`, `sync_error` / `sync_error_phase` when the last sync failed, and live `progress` (`phase`, `current`, `total`) while a sync runs. IDs that match no source are listed under `not_found`. Source config is never returned.
- **`trigger`** starts the given sources syncing in the background (at most 4 at once, same as **Sync selected** in the UI) and returns immediately with one outcome per ID: `started`, `already_syncing`, `disabled` or `not_found`. Poll `status` until `sync_status` is `completed` or `failed`.

```
sync_sources(request={"mode":"trigger","source_ids":["a1b2"]})
→ { "outcomes": { "a1b2": "started" } }
```

The tool can't yet tell whether a source is out of date with its upstream system; `last_synced_at` is the only freshness signal.

### `manage_sources`

Lists, reads, creates, updates and deletes API source configs, so an agent can set up a source for the user.

```
manage_sources(request: { mode: "list" })
manage_sources(request: { mode: "get", source_id: str })
manage_sources(request: { mode: "create", name: str, type: str, config: {...}, disabled?: bool })
manage_sources(request: { mode: "update", source_id: str, name?: str, type?: str, config?: {...}, disabled?: bool })
manage_sources(request: { mode: "delete", source_id: str, confirm?: bool })
```

- **`list`** returns every source (`id`, `name`, `type`, `provider`, `disabled`, `sync_status`), the stored **credential names** and the source types.
- **`get`** returns one source with its `config`.
- **`create`** / **`update`** take the same config keys as the UI and the export file (see [Sources](sources.md)). `config.Provider` is `api` (Azure DevOps, the default) or `github`, and decides which keys are allowed. `update` merges `config` into the existing one; `""` removes a key. Changing `type` moves the source to another collection and removes its old vectors. Saving never syncs — call `sync_sources` `trigger` afterwards.
- **`delete`** without `confirm: true` only reports what would go (`indexed_chunks`); with it, the source and its vectors are deleted.

Credentials are referenced by name only. `Token`, `Password` and `ApiKeyValue` must name a credential from `list`; anything else is rejected (and not echoed back), so a pasted secret is never stored. New credentials are added in the UI at `/credentials`.

Sources that are currently syncing can't be changed by the tool.

```
manage_sources(request={"mode":"create","name":"Bugs","type":"work-item",
  "config":{"Url":"https://ado.example/org/proj/_apis/wit/wiql?api-version=7.1","HttpMethod":"POST",
            "Body":"{\"query\":\"SELECT [System.Id] FROM WorkItems\"}","AuthType":"basic","Password":"ado-pat"}})
→ { "source": { "id": "c0ffee…", "name": "Bugs", "sync_status": "idle", "config": {...} }, "next": "Call sync_sources trigger with this id to index it." }
```

---

## Pull request tool

### `pull_request`

Reads Azure DevOps and GitHub pull requests **live**, for code review. It is read-only and nothing is synced or indexed, so results always reflect the pull request as it is now.

```
pull_request(request: { mode: "list", source_id: str, status?: "active"|"completed"|"abandoned"|"all", author?: str, reviewer?: str, target_branch?: str, page?: int })
pull_request(request: { mode: "get", url: str })
pull_request(request: { mode: "files", url: str, page?: int, from_iteration?: int, to_iteration?: int })
pull_request(request: { mode: "diff", url: str, path?: str, page?: int, include_content?: bool, from_iteration?: int, to_iteration?: int })
pull_request(request: { mode: "threads", url: str, page?: int })
pull_request(request: { mode: "iterations", url: str })
pull_request(request: { mode: "commits", url: str, page?: int })
```

**Credentials.** `url` is the pull request's web link: `…/{project}/_git/{repo}/pullrequest/{id}` (Azure DevOps Services or Server) or `…/{owner}/{repo}/pull/{number}` (github.com or GitHub Enterprise Server). Conduit uses the credentials of a configured source on the same organization or server. It prefers a source of the same repository, then of the same project or owner, then any source there. Between equally close sources, an enabled one wins. Every response says which source was used in `credentials_from`. A URL that no source matches is an error, so add a source for that organization first. `list` has no URL, so it takes the `source_id` of a source of the repository instead. For Azure DevOps that must be a code, test code, documentation or commit-history source.

- **`list`**: pull requests, newest first, 20 per page with `has_more`. `status` defaults to `active`. `author` and `reviewer` take `me` (the token's owner), an email (Azure DevOps) or a login (GitHub). On GitHub, `reviewer` matches pull requests whose review is still requested from that user.
- **`get`**: title, description, branches, merge status, head and base commits, reviewers with their votes, linked work items (on GitHub, the issues the description closes with `fixes #12`-style keywords), and checks (Azure DevOps statuses and branch policies; GitHub check runs and commit statuses). If work items or checks can't be read, `notes` says so and the rest is still returned.
- **`files`**: the changed files with change type and `additions`/`deletions`, 20 per page, plus `total_files`.
- **`diff`**: unified diffs from the merge base to head, for one `path` or a `page` of 20 files. `include_content: true` adds each file's full text at head, for the code around a change.
- **`threads`**: existing comment threads, 20 per page. Each has `kind` (`file` with `path`/`line`, `general`, or `review` for a GitHub review summary), `status` (Azure DevOps: `active`, `fixed`, …), and its comments with `reply_to`. Azure DevOps system comments (votes, pushes) are left out. GitHub's REST API doesn't report whether a thread is resolved; `outdated` marks comments on lines the latest changes replaced.
- **`iterations`**: the pull request's updates, which are pushes on Azure DevOps and commits on GitHub. `files` and `diff` accept `from_iteration` (0 = the merge base, the default) and `to_iteration` (default the latest), so `from_iteration: N` shows only what changed since iteration N.
- **`commits`**: the commits with full messages, 20 per page.

**Large pull requests** are paged, never truncated. Where GitHub itself leaves data out, the response says so in `incomplete`, either on the file or at the top level. GitHub omits patches for binary or very large files, lists at most 3000 files and 250 commits of a pull request, and compares at most 300 files between iterations. Azure DevOps diffs are built from the file contents, so every text file's diff is complete. On both providers, binary files show only their path and change type.

```
1. pull_request(request={"mode":"get","url":"https://dev.azure.com/acme/Shop/_git/web/pullrequest/12"})
2. pull_request(request={"mode":"files","url":"…/pullrequest/12"})            → total_files: 7
3. pull_request(request={"mode":"diff","url":"…/pullrequest/12","path":"/src/cart.ts","include_content":true})
4. pull_request(request={"mode":"threads","url":"…/pullrequest/12"})          → skip points already raised
```

---

## Usage patterns

**Start of every task:** `retrieve_experience` → search tools for context → do the work → `remember` anything learned.

**Scoping to one source** (e.g. two ADO repos):
```
search_source_code(request={"mode": "semantic_search", "query": "authentication middleware", "page": 1, "source_name": "Backend API"})
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

**Combining tools for a code change:** `retrieve_experience` → `search_source_code` (find the implementation) → `search_test_code` (find its tests) → `search_workitem` (related requirements or bugs).

**Reviewing a pull request:** `retrieve_experience` (review conventions) → `pull_request` `get` → `files` → `diff` per file → `threads` → `search_source_code` / `search_test_code` for callers and tests of what changed → `search_commit` for why the changed code was written that way.
