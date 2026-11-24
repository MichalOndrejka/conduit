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
| Versions | `Branch`, `Tags` | Files and commits. Comma-separated branches and tags, ticked from the repository's own (see [Versions](#versions-branches-and-tags)). Neither = the repository's default branch. |
| File filter | `PathFilter` | Files. Comma-separated wildcards, same as Azure DevOps. |
| Max items | `Top` | Commits and issues: newest first. |
| Fetch as | `FetchMode` | Requirements: `files` (default) or `workitems` (issues). |
| Issue types | `WorkItemTypes` | Issues. Comma-separated organization issue types (Bug, Feature, …), picked from tiles loaded from `GET /orgs/{owner}/issue-types`; an issue of any of them is fetched. Empty = every issue, typed or not. Combines with Labels: an issue needs one of the labels and one of the types. Personal accounts and GitHub Enterprise Server versions without issue types have none to pick. |
| Labels | `Labels` | Issues. Comma-separated; an issue with any of them is fetched. Empty = every issue. |
| Workflows | `Workflow` | Pipeline Runs. One or more workflow file names (`ci.yml`) or numeric IDs, comma-separated (`ci.yml, release.yml`). A listing reads each workflow's latest runs and merges them, newest first. |
| Runs | `Runs` | Pipeline Runs. How many of the latest runs `search_pipeline_run` lists when the agent doesn't ask for a number (default `5`, at most `100`). |
| Verify SSL | `VerifySSL` | `false` skips TLS verification (self-hosted servers with private CAs). |

What each source type fetches:

- **Source Code / Test Code / Documentation** (and Requirements in `files` mode): one document per file matching `PathFilter`, with its full content. Files over 200 KB or binary are noted as skipped. Each branch or tag is downloaded as a single tarball (`/repos/{owner}/{repo}/tarball/{ref}`) and read as it streams in, so a sync costs one API request per version, plus one to look up the default branch when none is ticked, whatever the repository size. A download cut off part-way is restarted.
- **Commit History**: one document per commit of each branch or tag (`Top` per version) with its full message and the patch of every changed file. GitHub returns no patch for binary or very large files and lists at most 3000 files per commit; the document notes either case.
- **Work Items** (and Requirements in `workitems` mode): open and closed issues with their bodies and every comment. Pull requests are left out; `search_pull_request` reads them live. Filters: `state`, `assigned_to`, `tags` (labels), `work_item_type` = the issue's type (Bug, Feature, …), or `Issue` when it has none.
- **Pipeline Runs**: not synced. The source names one or more workflows (`Workflow`) and `search_pipeline_run` reads their runs live — see [Pipeline Runs](#pipeline-runs). A run's details list each failed job of every attempt (so a run re-run to green still shows attempt 1's failures) with its failed steps, the failed jobs' annotations, the commits since the previous run of the workflow on the branch (compare API) and the artifacts it uploaded (metadata only). `steps` lists every job of every attempt and its steps, green ones too, and any job's log is read on request; logs GitHub has already deleted are noted. GitHub has no test results API, so there are no tests.

- **Pull Requests**: not synced; see [Pull Requests](#pull-requests).

The owner fills the `project` filter and the repository the `repository` filter, so search narrows GitHub and Azure DevOps sources the same way.

## Source types and collections

The source **type** only decides which Qdrant collection a source's documents land in — it has no effect on how documents are fetched (that's the provider's job).

| Source Type | Collection |
|-------------|------------|
| Work Items | `conduit_workitems` |
| Requirements (test cases included) | `conduit_requirements` |
| Commit History | `conduit_commits` |
| Pipeline Runs | — (read live, never synced) |
| Pull Requests | — (read live, never synced) |
| Source Code | `conduit_code` |
| Test Code | `conduit_testcode` |
| Documentation | `conduit_documentation` |

## Platform presets (UI convenience, not backend code)

The **Sources → Create** flow offers a friendly **Azure DevOps** tab. It's a frontend-only preset: filling in org/project/PAT/resource fields compiles them into the generic API keys above on submit (`Url`, `AuthType=basic`, `Password=<credential>`, `ItemsPath=value`, …), and the source is stored as an ordinary generic API source. Commit History, Source Code, Test Code and Documentation sources additionally get backend-side ADO enrichment (real diffs / real file content, see above); Work Items sources are fetched entirely server-side (see below). A few UI-only metadata keys (`Platform`, `AdoOrg`, `AdoProject`, `AdoApiVersion`, `AdoResource`, `AdoRepo`, `AdoQuery`) are persisted alongside the generic keys purely so the editor can re-open a source in the right tab, pre-filled.

Commit History sources on the Azure DevOps tab expose a **Max commits** field (`Top` config key, default `100`) instead of the raw `Top` key — Azure DevOps' commits-list endpoint caps a single response at 100 commits, so values above that are fetched by paging with `searchCriteria.$top`/`searchCriteria.$skip` rather than the generic `NextUrlPath` mechanism. They also take **Versions** (`Branch` and `Tags`; none = the repository's default branch, looked up from the repository), each listed with `searchCriteria.itemVersion.version` and `versionType` (`branch` or `tag`), **Max commits** apiece — without a version, ADO's commits endpoint walks every branch's history, duplicating commits shared with the default branch and pulling in stray feature-branch work.

Each commit's diff is built from its change list (`commits/{id}/changes`, one request per commit, more for commits over 100 files) and the old and new content of every changed file. That content is downloaded in **zips of up to 500 blobs** (`POST …/blobs` with the blob IDs, Azure DevOps' Get Blobs Zip), consecutive commits sharing a zip. It isn't fetched one request per file. A blob the zip doesn't hold under its ID is read on its own, so a server that names zip entries differently still gets complete diffs, only slower; the log says so. Top 100 commits with ~5 edited files each is about 1 list request, 100 change lists and 2 zips.

Source Code, Test Code and Documentation sources (and Requirements in Files mode) take **Versions** too, each applied as `versionDescriptor` (`versionType` `branch` or `tag`) so files and their web links come from that version; none = the repository's default branch, looked up so it can name the documents' version.

These sources download each version's whole tree as **one zip** (`…/items?path=/&$format=zip&download=true`) to a temporary file, then apply **File filter** (`PathFilter`) and **Max items** (`Top`, per version) locally. A sync costs one request per version for the files, whatever the repository size; a download cut off part-way is restarted. Each matching file becomes one document, ID `{source}_capi_{path}`, holding its path and full content; files over 200 KB or binary are noted as skipped. The form's preview downloads the whole zip too.

Within one sync batch (**Sync selected**, or `source_sync trigger` with several IDs), sources that read the same archive — the same repository, branch and credential, e.g. a Source Code and a Test Code source of one repository — share a single download: the first downloads, the others wait for it and read the same file. This holds for GitHub tarballs too. The files are deleted when the batch ends; nothing is kept between syncs, and a source synced on its own downloads for itself. A download that ends early (even when the response itself ended cleanly) is detected by reading the archive back and restarted.

The generic API config keys aren't tied to Azure DevOps — the backend runs any single-endpoint JSON API this way — but the web UI no longer exposes a form for building one from scratch; other API sources are added with the `source_manage` MCP tool, an import, or directly in `conduit-sources.json`. Example: **Jira**, `https://you.atlassian.net/rest/api/3/search` with `AuthType=basic`, `ItemsPath=issues`.

### Versions (branches and tags)

Every repository-backed source — Source Code, Test Code, Documentation, Requirements in Files mode and Commit History, on both providers — can embed several versions of the software side by side. The form's **Versions** picker has **Branches** and **Tags** sub-tabs listing the repository's own, loaded with the form's organization, project, repository and token (Azure DevOps `…/refs?filter=heads/` and `filter=tags/`; GitHub `/branches` and `/tags`): branches with the default first, tags newest version first. Tick in both to combine them; they're stored as `Branch` and `Tags`, comma-separated. None ticked = the repository's default branch. A saved version the repository no longer lists stays as its own ticked tile.

Each version is read in full, and what is the same in several is embedded once:
- A file with the same path and content in several versions is one document listing all of them. A path whose content differs gets one document per content, its ID and title suffixed with the first version holding it (`{source}_capi_{path}@v2.0`, title `path (v2.0, v2.1)`), linked to that version.
- A commit listed from several versions is one document with every version it was listed from. With **Max commits** set, that's the versions whose newest `Top` commits include it: an older commit beyond one version's limit is listed under the others only.

Documents carry their versions as the `version` property and the `version` filter of `search_source_code`, `search_test_code`, `search_documentation`, `search_requirement` and `search_commit` (see [mcp-tools.md](mcp-tools.md#versions)). Searches without it return every version.

### Azure DevOps Work Items

Work Items sources on the Azure DevOps tab don't use a hand-built URL at all: Conduit runs a WIQL query scoped to the configured project, then batch-fetches each matching work item's fields directly from Azure DevOps' Work Item Tracking API. The **Work item types** checkboxes restrict which types the WIQL query matches — `WorkItemTypes` config key, comma-separated; at least one type must be selected. **Area paths** further scopes the query to one or more team areas — `AreaPaths` config key, comma-separated (e.g. `MyProject\Team A, MyProject\Team B`), matched with WIQL's `UNDER` operator so sub-areas are included; left blank, every area in the project is fetched. Unlike the generic API source, work items always embed **every** fetched field — including custom project/organization fields — via Azure DevOps' `$expand`; `ContentFields` does not apply. Rich-text (HTML) field values such as Description, Repro Steps and Acceptance Criteria are stripped to plain text before embedding. Each document also gets a **Links** section built from the item's relations: parent, children and related items as `#id`, linked pull requests as `!id`, commits by SHA, builds by ID, branches and hyperlinks. Attached files get their own **Attachments** section: name, size, upload date, comment and link, taken from the relation's attributes. The files themselves are never downloaded (see [Attachments](#attachments)). It also gets the item's whole **Discussion**, fetched from the comments API (a preview API, so `AdoApiVersion` gets a `-preview` suffix) for each item with comments, oldest first. `System.History` is left out because it only holds the latest revision's comment. A discussion that can't be fetched fails the sync, so the previous index stays intact.

The type picker shows the project's own work item types, loaded from Azure DevOps (`_apis/wit/workitemtypes`) with the form's organization URL, project, API version and token as soon as they're filled in, so it matches whatever process the project uses: Agile, Scrum, CMMI, Basic or a custom inherited one. Disabled types and the ones Azure DevOps files under `Microsoft.HiddenCategory` (Test Plan, Test Suite, Shared Steps, code review and feedback types) are left out. A saved type the project no longer lists (renamed or removed) stays as its own ticked tile, so reopening a source never silently drops part of its selection.

### Azure DevOps Pipeline Runs

See [Pipeline Runs](#pipeline-runs). On the Azure DevOps tab a source takes `AdoOrg`, `AdoProject`, `AdoApiVersion` (default `7.1`), the pipelines, and **Runs** (`Runs`, optional, default `5`). The **Pipelines** sub-tabs choose the pipelines one of two ways:

- **IDs**: `DefinitionId`, one or more `definitionId`s from the pipelines' URLs, comma-separated (`42, 57`).
- **Folder**: `PipelineFolder`, a pipelines folder such as `\Release\Services` (`Release/Services` works too). Every pipeline in that folder and its subfolders counts; `\` is the whole project. The folder's pipelines are looked up when needed and kept for 5 minutes, so pipelines added to it later are included within that time. Azure DevOps filters by folder (`build/definitions?path={folder}&queryOrder=definitionNameAscending`, every page), so the project's other pipelines are never listed. If that listing holds no pipeline of a subfolder, the subfolders are read from the build folders API (a preview API) and each is listed the same way.

A source sets one of the two, never both. The runs of all its pipelines are listed together, newest first, in one `build/builds?definitions=…` call per 100 pipelines (keeping the URL under IIS's default query-string limit on Azure DevOps Server); with more than 100, each call's newest runs are merged and cut to the requested number. An agent targets one of them with `search_pipeline_run`'s `pipelines` mode and `list`'s `pipeline` filter (id or exact name), which asks for that pipeline's runs only — see [`search_pipeline_run`](mcp-tools.md#search_pipeline_run). `Url` is stored as `{org}/{project}` only so the form re-opens pre-filled.

### Requirements (dual-mode)

Requirements sources hold test cases too: there's no separate Test Cases type. Pick the Test Case work item type, or point the file filter at the test case files, and `search_requirement` finds them; an agent narrows to them with `work_item_type` or `path`. (The old `test-case` type and its `conduit_testcases` collection are gone: startup drops the collection, and a stored `test-case` source fails to sync until it's recreated as a Requirements source.)

Requirements sources can be fetched either way, since teams keep them as repo files (often `.md`) or as Azure DevOps work items interchangeably. The Azure DevOps tab shows a **Fetch as** toggle — `FetchMode` config key, `files` (default) or `workitems`:

- **Files** — same as Documentation: **Repository name**, **File filter** + **Versions** point at a repo items endpoint, and Conduit fetches real file content for each match (best-effort — falls back to raw metadata if the resource isn't a repo, e.g. it points at a wiki instead).
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

Pipeline Runs sources (`pipeline-run`) are **never synced**. One source names one or more pipelines — Azure DevOps build definitions (YAML or classic) by ID or by folder, or GitHub Actions workflows — and the credentials to read them. [`search_pipeline_run`](mcp-tools.md#search_pipeline_run) asks the provider on every call, so its answers are always current, and nothing about runs is stored in Qdrant. The Sources page marks such a source **Live**; it has no Sync button, and `source_sync trigger` answers `live_not_synced` for it.

What a call reads (Azure DevOps; GitHub reads the equivalents):

| Mode | Requests |
|---|---|
| `list` | one `build/builds?definitions=…&$top=N` call per 100 of the source's pipelines (after the folder's definitions listing, see [Azure DevOps Pipeline Runs](#azure-devops-pipeline-runs)), filtered by result, status, branch, reason and requester |
| `get` | the run itself, its timeline plus one per earlier attempt of a rerun job, its changes and artifacts; unless the run is green with a single attempt, also its test runs, the failed results of each test run whose statistics show failures (or don't say), the passed results of each test run that may hold retried tests, and the attachments of each test run. A failed or flaky test's own attachments are read only for the 20 tests of the page shown, one request each |
| `get` (context) | the pipeline definition at the run's revision (YAML file, variables) and the run's linked work items |
| `steps` | the run itself and its timeline plus one per earlier attempt of a rerun job |
| `tests` | the run itself, its timelines (to place each test in its attempt and job), its test runs and every result of each |
| `log` | the run itself and one step log |

A run's details cover every attempt: a run that went green after "Rerun failed jobs" still shows attempt 1's failed steps (from the previous-attempt timelines) and failed tests, and a test that passed only on an in-run retry (VSTest `rerunFailedTests` and the like, recorded as sub-results) is listed as flaky. Each failed step is the innermost failed timeline record with a log, so a job that failed without a failed task (a timeout) is listed too. Each test records the attempt and `Stage > Job` it belongs to: from its test run's pipeline reference, else from the job execution whose start/finish holds the test run; `attempt` 0 means it can't be told. Logs are cleaned of per-line timestamps and ANSI color codes; every line is kept.

**Cache.** Completed runs barely change, so the details, steps, tests, logs and test attachments of a **completed** run are kept in memory (up to 64 MB, least recently used out first), keyed by the run's finish time. The run's own state, which shows whether it was rerun since (a rerun gets a new finish time and is read again), is kept for 60 seconds, so paging through a cached run or log asks the provider nothing; after that a read costs one call. What was read of a run **still in progress** is kept for 30 seconds, so an agent polling a running build asks for its state on every call but for its details at most twice a minute. Listings always go to the provider. The cache lives for the process only.

**Rate limits.** A request refused for load (HTTP 429, 5xx, or GitHub's rate-limit 403) is retried up to 3 times, waiting as long as the server's `Retry-After` — or, with an exhausted limit, until its `X-RateLimit-Reset` — else 1, 2 and 4 seconds. A server that asks to wait longer than 30 seconds isn't asked again sooner: the call fails at once and says how long and until when. This applies to syncs too.

Nothing is dropped: `get` lists every failed step and every failed or flaky test (tests 20 per page), `steps` every stage/job/task of every attempt and `tests` every result whatever its outcome, and any step's whole log is readable in pages of 500 lines or searched. A request that still fails after retries fails the tool call; only a listing an older server lacks (HTTP 404, e.g. test attachments) becomes a note.

## Pull Requests

Pull Requests sources (`pull-request`) are **never synced**: a source names a repository and the credentials to read it, and [`search_pull_request`](mcp-tools.md#search_pull_request) lists and reads its pull requests live. Its `list` mode takes the source's id; every other mode takes a pull request URL and borrows the credentials of the closest configured source, so a Pull Requests source isn't required for those.

What can't change is kept in memory (up to 64 MB, least recently used out first), so reviewing a pull request file by file doesn't read it again for every file: each file version (by Azure DevOps blob id, or path at a commit), an iteration's changed files (Azure DevOps) or the changed files and commits at a head commit (GitHub). The pull request's iterations (Azure DevOps) or head commit (GitHub), which decide which of those apply, are kept for 60 seconds: a push shows up in `files` and `diff` within that time (`get` is always current). The identity behind `"me"` or an email in `list` is resolved once per process. The cache lives for the process only.

Config keys: the Azure DevOps tab takes `AdoOrg`, `AdoProject`, `AdoRepo` (required) and `AdoApiVersion`, and stores `Url` as `{org}/{project}/_apis/git/repositories/{repo}/pullrequests`. The GitHub tab takes the usual `GitHubUrl`, `GitHubOwner`, `GitHubRepo` and `Token`.

Earlier versions indexed pull requests and pipeline runs into `conduit_pull_requests` and `conduit_pipeline_runs`, and test cases into `conduit_testcases`; startup drops those collections.

## Attachments

Attached files and published artifacts are listed as **metadata only**: name, size, type, uploader, date, comment and link, never the contents. They can be zips of many gigabytes, and downloading them would stall syncs and fill the disk. This covers:

- work item attachments (indexed at sync)
- pipeline run artifacts (Azure DevOps and GitHub Actions; read live by `search_pipeline_run`)
- test run and test result attachments (Azure DevOps; read live by `search_pipeline_run`)

Each indexed list ends with a note telling the agent the files aren't searchable and that it should ask the user to download a file from its link and provide it when one matters. Conduit never fetches an attachment's contents. A server that lacks one of these listing APIs (HTTP 404) gets a note instead of failing the sync or tool call. Any other error fails it, as for the rest of the source.
