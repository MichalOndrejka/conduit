# Source Configuration Reference

Every source in Conduit has a **type** (which Qdrant collection it targets, see the table below) and a **provider** (which backend fetches the data). The backend is intentionally provider-agnostic — there is no ADO/Jira/GitHub-specific code. Any JSON-over-HTTP API is expressed as configuration through the single generic **HTTP API** provider; the **Manual** provider embeds pasted or uploaded content directly.

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
| Content fields | `ContentFields` | Comma-separated field names to include in the document body. Leave empty to include every field except the title field. |
| Next page path | `NextUrlPath` | Dot-notation path to a full next-page URL in the response, for pagination. |
| Top | `Top` | Maximum items to fetch across all pages. Default `500`. |
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

### Manual

Embeds content without any external connection. Set `Title` and `Content` directly (or upload a `.pdf`/`.txt`/`.md` file in the web UI, which extracts `Content` for you). The extracted text is stored in `conduit-sources.json` so preview and re-sync work without re-uploading.

On **export**, document content is replaced with a `__DOCUMENT_REQUIRED__` placeholder — anyone importing the config must open the source and provide the content again before syncing.

## Source types and collections

The source **type** only decides which Qdrant collection a source's documents land in — it has no effect on how documents are fetched (that's the provider's job).

| Source Type | Collection |
|-------------|------------|
| Work Items | `conduit_workitems` |
| Requirements | `conduit_requirements` |
| Test Cases | `conduit_testcases` |
| Test Results | `conduit_testresults` |
| Commit History | `conduit_commits` |
| Source Code | `conduit_code` |
| Test Code | `conduit_testcode` |
| Documentation | `conduit_documentation` |
| Build Results | `conduit_builds` |

Manual-provider sources always land in `conduit_documentation` regardless of type.

## Platform presets (UI convenience, not backend code)

The **Sources → Create** flow offers a friendly **Azure DevOps** tab. It's a frontend-only preset: filling in org/project/PAT/resource fields compiles them into the generic API keys above on submit (`Url`, `AuthType=basic`, `Password=<credential>`, `ItemsPath=value`, …), and the source is stored as an ordinary generic API source. Commit History, Source Code, Test Code and Documentation sources additionally get backend-side ADO enrichment (real diffs / real file content, see above); Work Items sources are fetched entirely server-side (see below). A few UI-only metadata keys (`Platform`, `AdoOrg`, `AdoProject`, `AdoApiVersion`, `AdoResource`, `AdoQuery`) are persisted alongside the generic keys purely so the editor can re-open a source in the right tab, pre-filled.

The generic API config keys aren't tied to Azure DevOps — the backend runs any single-endpoint JSON API this way — but the web UI no longer exposes a form for building one from scratch; non-ADO API sources must be added directly to `conduit-sources.json`. Examples expressible as pure configuration:

- **Jira**: `https://you.atlassian.net/rest/api/3/search` with `AuthType=basic`, `ItemsPath=issues`
- **GitHub**: `https://api.github.com/repos/{owner}/{repo}/issues` with `AuthType=bearer`

### Azure DevOps Work Items

Work Items sources on the Azure DevOps tab don't use a hand-built URL at all: Conduit runs a WIQL query scoped to the configured project, then batch-fetches each matching work item's fields directly from Azure DevOps' Work Item Tracking API. The **Work item types** checkboxes restrict which types the WIQL query matches — `WorkItemTypes` config key, comma-separated; at least one type must be selected. **Area paths** further scopes the query to one or more team areas — `AreaPaths` config key, comma-separated (e.g. `MyProject\Team A, MyProject\Team B`), matched with WIQL's `UNDER` operator so sub-areas are included; left blank, every area in the project is fetched. `ContentFields` still narrows which fetched fields get embedded, same as the generic API source.

The type picker shows a preset set of tiles per source type — Work Items: Epic, Feature, User Story, Bug, Task, Issue; Requirements: Product Requirement, Software Requirements, Risk, Architecture Item; Test Cases: Test Case — plus an **Add a custom type** input, since ADO process templates vary per project/org. Types added this way aren't limited to the presets; they're stored the same as a preset selection and re-appear as their own tile when the source is reopened for editing.

### Requirements and Test Cases (dual-mode)

Requirements and Test Cases sources can be fetched either way, since teams keep them as repo files (often `.md`) or as Azure DevOps work items interchangeably. The Azure DevOps tab shows a **Fetch as** toggle — `FetchMode` config key, `files` (default) or `workitems`:

- **Files in a repo** — same as Documentation: **Resource path** + **File filter** point at a repo items endpoint, and Conduit fetches real file content for each match (best-effort — falls back to raw metadata if the resource isn't a repo, e.g. it points at a wiki instead).
- **Azure DevOps work items** — identical to the Work Items source above, including the preset/custom type picker: **Area paths** + **Work item types** drive a WIQL query, `WorkItemTypes` requires at least one type selected.

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
