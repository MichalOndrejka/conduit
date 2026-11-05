# Source Configuration Reference

Every source in Conduit has a **type** (which Qdrant collection it targets, see the table below) and a **provider** (which backend fetches the data). Azure DevOps sources run on the generic **HTTP API** provider (`Provider` = `api`) with ADO-specific enrichment; **GitHub** sources (`Provider` = `github`) have their own fetcher.

## Providers

### HTTP API

Fetches from any endpoint that returns JSON. Configured via `internal/sources/api.go`.

| Field | Config key | Description |
|-------|-----------|-------------|
| URL | `Url` | Endpoint returning JSON. Required. |
| HTTP method | `HttpMethod` | `GET` (default) or `POST`. |
| Body | `Body` | Raw JSON request body, for `POST` endpoints. |
| Headers | `Headers` | Extra request headers, one `Name: value` per line. |
| Auth type | `AuthType` | `none` (default) \| `bearer` \| `basic` \| `apikey` |
| Token | `Token` | Credential name (from `/credentials`). Used for bearer auth. |
| Username | `Username` | Plain-text username for basic auth. May be empty — e.g. Azure DevOps PATs go in the password slot with a blank username. |
| Password | `Password` | Credential name. Used as the basic-auth password. |
| API key header | `ApiKeyHeader` | Header name for API-key auth. Defaults to `X-Api-Key`. |
| API key value | `ApiKeyValue` | Credential name. Used as the API-key header value. |
| Items path | `ItemsPath` | Dot-notation path into the response JSON to the array of items, e.g. `value` or `data.records`. Leave empty if the root is the array. |
| ID field | `IdField` | Item field used to build stable document IDs. Defaults to the item's position in the response. |
| Title field | `TitleField` | Field used as the document title. Defaults to `title`. |
| Web URL field | `WebUrlField` | Dot-notation path to the item's browser (UI) link, stored as the `web_url` property that search results cite. Defaults to the first of `_links.html.href`, `_links.web.href`, `remoteUrl`, `webUrl`, `html_url`, `web_url` present on the item (API URLs containing `/_apis/` are ignored). Azure DevOps repo files and commits without one get a link built from `Url`. |
| Content fields | `ContentFields` | Comma-separated field names to include in the document body. Leave empty to include every field except the title field. |
| Next page path | `NextUrlPath` | Dot-notation path to a full next-page URL in the response, for pagination. |
| Top | `Top` | Maximum items to fetch across all pages. Default: unlimited (every item the source has, up to 50 pages). |
| Verify SSL | `VerifySSL` | Set to `false` to skip TLS verification (self-hosted instances with private CAs). |

Credential fields (`Token`, `Password`, `ApiKeyValue`) hold a **name**, not the secret itself — the actual value lives in the encrypted [credential library](/credentials) and is looked up at sync time.

**How item mapping works**

1. The response JSON is navigated to `ItemsPath` (or the root if empty).
2. If the result is a single object rather than an array, it is wrapped in a list.
3. For each item, `TitleField` becomes the document title; falls back to `IdField`'s value, then to a positional `Item N`.
4. If `ContentFields` is set, only those fields appear in the body text. Otherwise every field except the title field is included.
5. Document IDs are `{source_id}_capi_{IdField value or position}`.
6. If `NextUrlPath` resolves to a string, that URL is fetched next — up to 50 pages or `Top` items, whichever comes first.

Constraint accepted by this generic design: multi-step fetch flows (e.g. an ADO WIQL query followed by a batch fetch, or downloading a git repo as a zip and parsing source files into code units) aren't expressible — only single-endpoint JSON APIs are.

### GitHub

Fetches from github.com or a GitHub Enterprise Server through the GitHub REST API (`internal/sources/github*.go`). Stored with `Provider` = `github`; picked on the **GitHub** tab of the source form.

| Field | Config key | Description |
|-------|-----------|-------------|
| Server URL | `GitHubUrl` | `https://github.com` (default), your GitHub Enterprise Server, e.g. `https://github.example.com` (the API is then `…/api/v3`), or a GitHub Enterprise Cloud tenant with data residency, e.g. `https://octo.ghe.com` (the API is then `https://api.octo.ghe.com`). |
| Owner | `GitHubOwner` | User or organization. Required. |
| Repository | `GitHubRepo` | Repository name. Required. |
| Token | `Token` | Credential name of a personal access token (classic or fine-grained) with read access to contents, issues and actions as needed. Empty = public repositories only, at GitHub's low anonymous rate limit. |
| Branch | `Branch` | Files and commits. Empty = the repository's default branch. |
| File filter | `PathFilter` | Files. Comma-separated wildcards, same as Azure DevOps. |
| Max items | `Top` | Commits and issues: newest first. |
| Fetch as | `FetchMode` | Requirements / Test Cases: `files` (default) or `workitems` (issues). |
| Issue types | `WorkItemTypes` | Issues. Comma-separated organization issue types (Bug, Feature, …), picked from tiles loaded from `GET /orgs/{owner}/issue-types`; an issue of any of them is fetched. Empty = every issue, typed or not. Combines with Labels: an issue needs one of the labels and one of the types. Personal accounts and GitHub Enterprise Server versions without issue types have none to pick. |
| Labels | `Labels` | Issues. Comma-separated; an issue with any of them is fetched. Empty = every issue. |
| Workflow | `Workflow` | Pipeline Runs. Workflow file name (`ci.yml`) or numeric ID. |
| Days | `Days` | Pipeline Runs. Completed runs created in the last N days. |
| Verify SSL | `VerifySSL` | `false` skips TLS verification (self-hosted servers with private CAs). |

What each source type fetches:

- **Source Code / Test Code / Documentation** (and Requirements / Test Cases in `files` mode): one document per file matching `PathFilter`, with its full content. Files over 200 KB or binary are noted as skipped. The branch is downloaded as a single tarball (`/repos/{owner}/{repo}/tarball/{branch}`) and read as it streams in, so a sync costs one API request, plus one to look up the default branch when `Branch` is empty, whatever the repository size. A download cut off part-way is restarted.
- **Commit History**: one document per commit with its full message and the patch of every changed file. GitHub returns no patch for binary or very large files and lists at most 3000 files per commit; the document notes either case.
- **Work Items** (and Requirements / Test Cases in `workitems` mode): open and closed issues with their bodies and every comment. Pull requests are left out. Filters: `state`, `assigned_to`, `tags` (labels), `work_item_type` = the issue's type (Bug, Feature, …), or `Issue` when it has none.
- **Pipeline Runs**: one document per completed workflow run; a failed run (failure, timed_out, startup_failure) also lists its failed jobs' annotations and yields one document per failed job with the complete job log (`parent_doc_id` links it to the run). Logs GitHub has already deleted are noted. GitHub returns at most 1000 runs per query; a window holding more fails the sync and asks for fewer Days.

The owner fills the `project` filter and the repository the `repository` filter, so search narrows GitHub and Azure DevOps sources the same way.

## Source types and collections

The source **type** only decides which Qdrant collection a source's documents land in — it has no effect on how documents are fetched (that's the provider's job).

| Source Type | Collection |
|-------------|------------|
| Work Items | `conduit_workitems` |
| Requirements | `conduit_requirements` |
| Test Cases | `conduit_testcases` |
| Commit History | `conduit_commits` |
| Pipeline Runs | `conduit_pipeline_runs` |
| Source Code | `conduit_code` |
| Test Code | `conduit_testcode` |
| Documentation | `conduit_documentation` |

## Platform presets (UI convenience, not backend code)

The **Sources → Create** flow offers a friendly **Azure DevOps** tab. It's a frontend-only preset: filling in org/project/PAT/resource fields compiles them into the generic API keys above on submit (`Url`, `AuthType=basic`, `Password=<credential>`, `ItemsPath=value`, …), and the source is stored as an ordinary generic API source. Commit History, Source Code, Test Code and Documentation sources additionally get backend-side ADO enrichment (real diffs / real file content, see above); Work Items sources are fetched entirely server-side (see below). A few UI-only metadata keys (`Platform`, `AdoOrg`, `AdoProject`, `AdoApiVersion`, `AdoResource`, `AdoRepo`, `AdoQuery`) are persisted alongside the generic keys purely so the editor can re-open a source in the right tab, pre-filled.

Commit History sources on the Azure DevOps tab expose a **Max commits** field (`Top` config key, default `100`) instead of the raw `Top` key — Azure DevOps' commits-list endpoint caps a single response at 100 commits, so values above that are fetched by paging with `searchCriteria.$top`/`searchCriteria.$skip` rather than the generic `NextUrlPath` mechanism. They also expose a **Branch** field (`Branch` config key; empty = the repository's default branch, looked up from the repository), applied as `searchCriteria.itemVersion.version` — without it, ADO's commits endpoint walks every branch's history, duplicating commits shared with the default branch and pulling in stray feature-branch work.

Source Code, Test Code and Documentation sources (and Requirements / Test Cases in Files mode) have an optional **Branch** field too (`Branch` config key), applied to the items listing as `versionDescriptor` so files and their web links come from that branch; empty = the repository's default branch.

The generic API config keys aren't tied to Azure DevOps — the backend runs any single-endpoint JSON API this way — but the web UI no longer exposes a form for building one from scratch; non-ADO API sources must be added directly to `conduit-sources.json`. Examples expressible as pure configuration:

- **Jira**: `https://you.atlassian.net/rest/api/3/search` with `AuthType=basic`, `ItemsPath=issues`
- **GitHub**: `https://api.github.com/repos/{owner}/{repo}/issues` with `AuthType=bearer`

### Azure DevOps Work Items

Work Items sources on the Azure DevOps tab don't use a hand-built URL at all: Conduit runs a WIQL query scoped to the configured project, then batch-fetches each matching work item's fields directly from Azure DevOps' Work Item Tracking API. The **Work item types** checkboxes restrict which types the WIQL query matches — `WorkItemTypes` config key, comma-separated; at least one type must be selected. **Area paths** further scopes the query to one or more team areas — `AreaPaths` config key, comma-separated (e.g. `MyProject\Team A, MyProject\Team B`), matched with WIQL's `UNDER` operator so sub-areas are included; left blank, every area in the project is fetched. Unlike the generic API source, work items always embed **every** fetched field — including custom project/organization fields — via Azure DevOps' `$expand`; `ContentFields` does not apply. Rich-text (HTML) field values such as Description, Repro Steps and Acceptance Criteria are stripped to plain text before embedding.

The type picker shows the project's own work item types, loaded from Azure DevOps (`_apis/wit/workitemtypes`) with the form's organization URL, project, API version and token as soon as they're filled in, so it matches whatever process the project uses: Agile, Scrum, CMMI, Basic or a custom inherited one. Disabled types and the ones Azure DevOps files under `Microsoft.HiddenCategory` (Test Plan, Test Suite, Shared Steps, code review and feedback types) are left out. A saved type the project no longer lists (renamed or removed) stays as its own ticked tile, so reopening a source never silently drops part of its selection.

### Azure DevOps Pipeline Runs

Pipeline Runs sources (`pipeline-run`) are Azure DevOps-only and fetched entirely server-side — one source covers one build definition (YAML or classic). Config keys: `AdoOrg`, `AdoProject`, `AdoApiVersion` (default `7.1`), **Pipeline definition ID** (`DefinitionId`, the `definitionId` in the pipeline's URL, required) and **Days** (`Days`, required, form default `30`). `Url` is stored as `{org}/{project}` only so the form re-opens pre-filled.

Each sync lists every **completed** run of the definition that finished in the last `Days` days — all branches, including PR validation runs — following ADO's `x-ms-continuationtoken` paging, and fully replaces the source's previous documents. The index is a snapshot of the last successful sync: runs, logs and test results deleted from Azure DevOps afterwards stay searchable (their `web_url` may then be stale) until the next successful sync.

Documents (all in `conduit_pipeline_runs`):

| Document | ID | When | Text |
|---|---|---|---|
| Run | `{source}_run_{buildId}` | every run | Pipeline, run number, result, status, reason, branch, triggering commit, trigger info, requester, timing; for failed / partially succeeded runs also every timeline error/warning (with its `Stage > Job > Task` path) and the IDs of its step and test documents |
| Step | `{source}_run_{buildId}_step_{timelineRecordId}` | failed / partially succeeded runs | The **complete** log of each failed timeline record that has no failed child (so a job that failed without a failed task, e.g. a timeout, is captured too). Per-line timestamps and ANSI color codes are stripped; every line is kept |
| Test | `{source}_run_{buildId}_test_{testRunId}_{resultId}` | failed / partially succeeded runs | Each test result with outcome Failed, Aborted or Error: name, storage, test run, duration, error message and full stack trace |

Properties: `type` (`run`, `step`, `test`), `build_id`, `result`, `branch`, `commit`, `project`, `repository`, `web_url`; step and test documents also carry `parent_doc_id` (the run document's ID) and steps `step_path`; run documents of Azure Repos runs carry `commit_web_url`. Every chunk's text starts with a `[Run #<number> > Stage > Job > Task | chunk k/N]` header (`[Run #<number> > test <name> | …]` for tests, `[Run #<number> | …]` for runs), so a chunk deep inside a long log still names the run and step it belongs to.

Fetching is lossless: any run, timeline, log or test-result request that still fails after retries — including a 404 for a log Azure DevOps already purged — fails the sync, leaving the previous snapshot untouched.

### Requirements and Test Cases (dual-mode)

Requirements and Test Cases sources can be fetched either way, since teams keep them as repo files (often `.md`) or as Azure DevOps work items interchangeably. The Azure DevOps tab shows a **Fetch as** toggle — `FetchMode` config key, `files` (default) or `workitems`:

- **Files** — same as Documentation: **Repository name**, **Branch** + **File filter** point at a repo items endpoint, and Conduit fetches real file content for each match (best-effort — falls back to raw metadata if the resource isn't a repo, e.g. it points at a wiki instead).
- **Work Items** — identical to the Work Items source above, including the project's type picker: **Area paths** + **Work item types** drive a WIQL query, `WorkItemTypes` requires at least one type selected.

Switching the toggle swaps which field set is submitted; the other is disabled client-side so its stale values aren't persisted.

## Common patterns

### Targeting multiple sources of the same type

Create one source per system/repository. Use the `source_name` filter on MCP search tools to limit results to a specific one.

### Paginated APIs

Set `NextUrlPath` to the response field holding the next page's full URL (many APIs, including ADO's continuation-token endpoints reshaped as a URL, and GitHub's `Link`-less JSON-body pagination, expose this). Set `Top` to cap total items fetched.

### Private API with a custom auth header

Set `AuthType` to `apikey`, `ApiKeyHeader` to the header name your API expects (e.g. `X-Api-Key`), and create a credential at `/credentials` holding the key value for `ApiKeyValue`.

## What's not expressible

Because the backend is a single generic HTTP-JSON provider, some fetch patterns are out of reach by design:

- Multi-step flows — e.g. a WIQL query followed by a batch fetch of matching items.
- Downloading a git repository as a zip and parsing individual files into code units (classes, methods, functions).
- Recursive wiki/tree-walk fetches.

These require dedicated provider code, which the Go backend deliberately does not have (see [go-port.md](go-port.md) for the rationale).
