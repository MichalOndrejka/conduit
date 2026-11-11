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

Credential fields (`Token`, `Password`, `ApiKeyValue`) hold a **name**, not the secret itself — the actual value lives in the encrypted credential library (`/credentials` in the UI) and is looked up at sync time.

**How item mapping works**

1. The response JSON is navigated to `ItemsPath` (or the root if empty).
2. If the result is a single object rather than an array, it is wrapped in a list.
3. For each item, `TitleField` becomes the document title; falls back to `IdField`'s value, then to a positional `Item N`.
4. If `ContentFields` is set, only those fields appear in the body text. Otherwise every field except the title field is included.
5. Document IDs are `{source_id}_capi_{IdField value or position}`.
6. If `NextUrlPath` resolves to a string, that URL is fetched next — up to 50 pages or `Top` items, whichever comes first.

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
| Runs | `Runs` | Pipeline Runs. How many of the latest runs `search_pipeline_run` lists when the agent doesn't ask for a number (default `5`, at most `100`). |
| Verify SSL | `VerifySSL` | `false` skips TLS verification (self-hosted servers with private CAs). |

What each source type fetches:

- **Source Code / Test Code / Documentation** (and Requirements / Test Cases in `files` mode): one document per file matching `PathFilter`, with its full content. Files over 200 KB or binary are noted as skipped. The branch is downloaded as a single tarball (`/repos/{owner}/{repo}/tarball/{branch}`) and read as it streams in, so a sync costs one API request, plus one to look up the default branch when `Branch` is empty, whatever the repository size. A download cut off part-way is restarted.
- **Commit History**: one document per commit with its full message and the patch of every changed file. GitHub returns no patch for binary or very large files and lists at most 3000 files per commit; the document notes either case.
- **Work Items** (and Requirements / Test Cases in `workitems` mode): open and closed issues with their bodies and every comment. Pull requests are left out; `search_pull_request` reads them live. Filters: `state`, `assigned_to`, `tags` (labels), `work_item_type` = the issue's type (Bug, Feature, …), or `Issue` when it has none.
- **Pipeline Runs**: not synced. The source names the workflow (`Workflow`) and `search_pipeline_run` reads its runs live — see [Pipeline Runs](#pipeline-runs). A run's details list each failed job of every attempt (so a run re-run to green still shows attempt 1's failures) with its failed steps, the failed jobs' annotations, the commits since the previous run of the workflow on the branch (compare API) and the artifacts it uploaded (metadata only). A job's log is read on request; logs GitHub has already deleted are noted. GitHub has no test results API, so there are no tests.

- **Pull Requests**: not synced; see [Pull Requests](#pull-requests).

The owner fills the `project` filter and the repository the `repository` filter, so search narrows GitHub and Azure DevOps sources the same way.

## Source types and collections

The source **type** only decides which Qdrant collection a source's documents land in — it has no effect on how documents are fetched (that's the provider's job).

| Source Type | Collection |
|-------------|------------|
| Work Items | `conduit_workitems` |
| Requirements | `conduit_requirements` |
| Test Cases | `conduit_testcases` |
| Commit History | `conduit_commits` |
| Pipeline Runs | — (read live, never synced) |
| Pull Requests | — (read live, never synced) |
| Source Code | `conduit_code` |
| Test Code | `conduit_testcode` |
| Documentation | `conduit_documentation` |

## Platform presets (UI convenience, not backend code)

The **Sources → Create** flow offers a friendly **Azure DevOps** tab. It's a frontend-only preset: filling in org/project/PAT/resource fields compiles them into the generic API keys above on submit (`Url`, `AuthType=basic`, `Password=<credential>`, `ItemsPath=value`, …), and the source is stored as an ordinary generic API source. Commit History, Source Code, Test Code and Documentation sources additionally get backend-side ADO enrichment (real diffs / real file content, see above); Work Items sources are fetched entirely server-side (see below). A few UI-only metadata keys (`Platform`, `AdoOrg`, `AdoProject`, `AdoApiVersion`, `AdoResource`, `AdoRepo`, `AdoQuery`) are persisted alongside the generic keys purely so the editor can re-open a source in the right tab, pre-filled.

Commit History sources on the Azure DevOps tab expose a **Max commits** field (`Top` config key, default `100`) instead of the raw `Top` key — Azure DevOps' commits-list endpoint caps a single response at 100 commits, so values above that are fetched by paging with `searchCriteria.$top`/`searchCriteria.$skip` rather than the generic `NextUrlPath` mechanism. They also expose a **Branch** field (`Branch` config key; empty = the repository's default branch, looked up from the repository), applied as `searchCriteria.itemVersion.version` — without it, ADO's commits endpoint walks every branch's history, duplicating commits shared with the default branch and pulling in stray feature-branch work.

Each commit's diff is built from its change list (`commits/{id}/changes`, one request per commit, more for commits over 100 files) and the old and new content of every changed file. That content is downloaded in **zips of up to 500 blobs** (`POST …/blobs` with the blob IDs, Azure DevOps' Get Blobs Zip), consecutive commits sharing a zip. It isn't fetched one request per file. A blob the zip doesn't hold under its ID is read on its own, so a server that names zip entries differently still gets complete diffs, only slower; the log says so. Top 100 commits with ~5 edited files each is about 1 list request, 100 change lists and 2 zips.

Source Code, Test Code and Documentation sources (and Requirements / Test Cases in Files mode) have an optional **Branch** field too (`Branch` config key), applied as `versionDescriptor` so files and their web links come from that branch; empty = the repository's default branch.

These sources download the branch's whole tree as **one zip** (`…/items?path=/&recursionLevel=Full&$format=zip&download=true`) to a temporary file, then apply **File filter** (`PathFilter`) and **Max items** (`Top`) locally. A sync costs one request for the files, whatever the repository size; a download cut off part-way is restarted. Each matching file becomes one document, ID `{source}_capi_{path}`, holding its path and full content; files over 200 KB or binary are noted as skipped. The form's preview downloads the whole zip too.

Within one sync batch (**Sync selected**, or `source_sync trigger` with several IDs), sources that read the same archive — the same repository, branch and credential, e.g. a Source Code and a Test Code source of one repository — share a single download: the first downloads, the others wait for it and read the same file. This holds for GitHub tarballs too. The files are deleted when the batch ends; nothing is kept between syncs, and a source synced on its own downloads for itself. A download that ends early (even when the response itself ended cleanly) is detected by reading the archive back and restarted.

The generic API config keys aren't tied to Azure DevOps — the backend runs any single-endpoint JSON API this way — but the web UI no longer exposes a form for building one from scratch; other API sources are added with the `source_manage` MCP tool, an import, or directly in `conduit-sources.json`. Example: **Jira**, `https://you.atlassian.net/rest/api/3/search` with `AuthType=basic`, `ItemsPath=issues`.

### Azure DevOps Work Items

Work Items sources on the Azure DevOps tab don't use a hand-built URL at all: Conduit runs a WIQL query scoped to the configured project, then batch-fetches each matching work item's fields directly from Azure DevOps' Work Item Tracking API. The **Work item types** checkboxes restrict which types the WIQL query matches — `WorkItemTypes` config key, comma-separated; at least one type must be selected. **Area paths** further scopes the query to one or more team areas — `AreaPaths` config key, comma-separated (e.g. `MyProject\Team A, MyProject\Team B`), matched with WIQL's `UNDER` operator so sub-areas are included; left blank, every area in the project is fetched. Unlike the generic API source, work items always embed **every** fetched field — including custom project/organization fields — via Azure DevOps' `$expand`; `ContentFields` does not apply. Rich-text (HTML) field values such as Description, Repro Steps and Acceptance Criteria are stripped to plain text before embedding. Each document also gets a **Links** section built from the item's relations: parent, children and related items as `#id`, linked pull requests as `!id`, commits by SHA, builds by ID, branches and hyperlinks. Attached files get their own **Attachments** section: name, size, upload date, comment and link, taken from the relation's attributes. The files themselves are never downloaded (see [Attachments](#attachments)). It also gets the item's whole **Discussion**, fetched from the comments API (a preview API, so `AdoApiVersion` gets a `-preview` suffix) for each item with comments, oldest first. `System.History` is left out because it only holds the latest revision's comment. A discussion that can't be fetched fails the sync, so the previous index stays intact.

The type picker shows the project's own work item types, loaded from Azure DevOps (`_apis/wit/workitemtypes`) with the form's organization URL, project, API version and token as soon as they're filled in, so it matches whatever process the project uses: Agile, Scrum, CMMI, Basic or a custom inherited one. Disabled types and the ones Azure DevOps files under `Microsoft.HiddenCategory` (Test Plan, Test Suite, Shared Steps, code review and feedback types) are left out. A saved type the project no longer lists (renamed or removed) stays as its own ticked tile, so reopening a source never silently drops part of its selection.

### Azure DevOps Pipeline Runs

See [Pipeline Runs](#pipeline-runs). On the Azure DevOps tab a source takes `AdoOrg`, `AdoProject`, `AdoApiVersion` (default `7.1`), **Pipeline definition ID** (`DefinitionId`, the `definitionId` in the pipeline's URL, required) and **Runs** (`Runs`, optional, default `5`). `Url` is stored as `{org}/{project}` only so the form re-opens pre-filled.

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

## Pipeline Runs

Pipeline Runs sources (`pipeline-run`) are **never synced**. One source names one pipeline — an Azure DevOps build definition (YAML or classic) or a GitHub Actions workflow — and the credentials to read it. [`search_pipeline_run`](mcp-tools.md#search_pipeline_run) asks the provider on every call, so its answers are always current, and nothing about runs is stored in Qdrant. The Sources page marks such a source **Live**; it has no Sync button, and `source_sync trigger` answers `live_not_synced` for it.

What a call reads (Azure DevOps; GitHub reads the equivalents):

| Mode | Requests |
|---|---|
| `list` | one `build/builds?definitions=…&$top=N` call, filtered by result, status, branch, reason and requester |
| `get` | the run itself, its timeline plus one per earlier attempt of a rerun job, its changes and artifacts; unless the run is green with a single attempt, also its test runs, the failed results of each test run whose statistics show failures (or don't say), the passed results of each test run that may hold retried tests, and the attachments of each test run and failed or flaky test |
| `log` | the run itself and one step log |

A run's details cover every attempt: a run that went green after "Rerun failed jobs" still shows attempt 1's failed steps (from the previous-attempt timelines) and failed tests, and a test that passed only on an in-run retry (VSTest `rerunFailedTests` and the like, recorded as sub-results) is listed as flaky. Each failed step is the innermost failed timeline record with a log, so a job that failed without a failed task (a timeout) is listed too. Each test records the attempt and `Stage > Job` it belongs to: from its test run's pipeline reference, else from the job execution whose start/finish holds the test run; `attempt` 0 means it can't be told. Logs are cleaned of per-line timestamps and ANSI color codes; every line is kept.

**Cache.** Completed runs barely change, so the details and logs of a **completed** run are kept in memory (up to 64 MB, least recently used out first), keyed by the run's finish time. Reading a cached run still costs one call — for the run itself, to see whether it was rerun since; a rerun gets a new finish time and is read again. Listings and runs still in progress always go to the provider. The cache lives for the process only.

Nothing is dropped: every failed step and every failed or flaky test is listed (tests 20 per page), and a step's whole log is readable in pages of 500 lines. A request that still fails after retries fails the tool call; only a listing an older server lacks (HTTP 404, e.g. test attachments) becomes a note.

## Pull Requests

Pull Requests sources (`pull-request`) are **never synced**: a source names a repository and the credentials to read it, and [`search_pull_request`](mcp-tools.md#search_pull_request) lists and reads its pull requests live. Its `list` mode takes the source's id; every other mode takes a pull request URL and borrows the credentials of the closest configured source, so a Pull Requests source isn't required for those.

Config keys: the Azure DevOps tab takes `AdoOrg`, `AdoProject`, `AdoRepo` (required) and `AdoApiVersion`, and stores `Url` as `{org}/{project}/_apis/git/repositories/{repo}/pullrequests`. The GitHub tab takes the usual `GitHubUrl`, `GitHubOwner`, `GitHubRepo` and `Token`.

Earlier versions indexed pull requests and pipeline runs into `conduit_pull_requests` and `conduit_pipeline_runs`; startup drops those collections.

## Attachments

Attached files and published artifacts are listed as **metadata only**: name, size, type, uploader, date, comment and link, never the contents. They can be zips of many gigabytes, and downloading them would stall syncs and fill the disk. This covers:

- work item attachments (indexed at sync)
- pipeline run artifacts (Azure DevOps and GitHub Actions; read live by `search_pipeline_run`)
- test run and test result attachments (Azure DevOps; read live by `search_pipeline_run`)

Each indexed list ends with a note telling the agent the files aren't searchable and that it should ask the user to download a file from its link and provide it when one matters. Conduit never fetches an attachment's contents. A server that lacks one of these listing APIs (HTTP 404) gets a note instead of failing the sync or tool call. Any other error fails it, as for the rest of the source.
