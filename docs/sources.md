# Source Configuration Reference

Every source in Conduit has a **type** (which Qdrant collection it targets) and a **provider** (which backend fetches the data). The provider is selected using the tab switcher on the configure page.

## Providers

### Azure DevOps

Fetches data from an ADO REST API — cloud or on-premise TFS/VSTS.

**Connection fields**

| Field | Config key | Description |
|-------|-----------|-------------|
| Base URL | `BaseUrl` | Full project URL, e.g. `https://dev.azure.com/org/project` or `https://tfs.company.com/DefaultCollection/MyProject` |
| Auth type | `AuthType` | `pat` \| `bearer` \| `ntlm` \| `negotiate` \| `apikey` \| `none` |
| API version | `ApiVersion` | Defaults to `7.1`. Change only if your TFS requires an older version. |

**Auth type details**

| Auth type | Additional fields | Notes |
|-----------|------------------|-------|
| `pat` | `Pat` (env var name) | Personal Access Token. Recommended for ADO cloud. |
| `bearer` | `Token` (env var name) | Bearer token. Suitable for service principals. |
| `ntlm` | `Username`, `Password` (env var name), `Domain` | On-premise TFS with Windows/NTLM auth. |
| `negotiate` | `Username`, `Password` (env var name), `Domain` | Kerberos/NTLM negotiate. Falls back to NTLM if `requests-negotiate-sspi` is not installed. |
| `apikey` | `ApiKeyHeader`, `ApiKeyValue` (env var name) | Sends a custom header, e.g. `X-Api-Key: <value>`. |
| `none` | — | No authentication. For open endpoints or when auth is handled at network level. |

Credential fields (`Pat`, `Token`, `Password`, `ApiKeyValue`) store the **name of an environment variable**, not the secret itself. Conduit reads the actual value from the environment at sync time. If the named variable is not set, the literal string is used as a fallback (useful for testing).

---

### Custom API

Fetches from any HTTP endpoint that returns JSON.

| Field | Config key | Description |
|-------|-----------|-------------|
| URL | `Url` | Full endpoint URL. Required. |
| HTTP method | `HttpMethod` | `GET` (default) or `POST`. |
| Auth type | `AuthType` | `none` (default) \| `bearer` \| `apikey` |
| Token | `Token` | Env var name holding the bearer token. |
| API key header | `ApiKeyHeader` | Header name for API key auth, e.g. `X-Api-Key`. |
| API key value | `ApiKeyValue` | Env var name holding the API key value. |
| Items path | `ItemsPath` | Dot-notation path into the response JSON to the array of items, e.g. `data.records`. Leave empty if the root is an array. |
| Title field | `TitleField` | Field name used as the document title. Defaults to `title`. |
| Content fields | `ContentFields` | Comma-separated field names to include in the document body. Leave empty to include all fields. |

**How item mapping works**

1. Response JSON is navigated to `ItemsPath` (or the root if empty).
2. If the result is a single object rather than an array, it is wrapped in a list.
3. For each item, `TitleField` becomes the document title.
4. If `ContentFields` is set, only those fields appear in the body text. Otherwise all fields except `TitleField` are included.
5. Document IDs are `{source_id}_capi_{index}`.

---

### Manual

Embeds content without any external connection.

| Sub-tab | Config keys | Description |
|---------|------------|-------------|
| Text | `Title`, `Content` | Paste a title and body text directly. |
| File upload | `Title`, `Content` (extracted) | Upload a `.pdf`, `.txt`, or `.md` file. Drag-and-drop supported. |

The extracted text is stored in `conduit-sources.json` so preview and re-sync work without re-uploading.

---

## Source types and their ADO data

### Work Items (`workitem-query`)

Fetches work items via WIQL query.

| Config key | Description | Default |
|-----------|-------------|---------|
| `Query` | Full WIQL query. If set, overrides all other filters. | — |
| `ItemTypes` | Comma-separated work item types, e.g. `Bug, Task, User Story`. | `Bug, Task, User Story, Feature, Epic` |
| `AreaPath` | Limit to items under this area path (UNDER clause). | — |
| `Fields` | Comma-separated fields to fetch, e.g. `System.Title, System.State`. | All fields |

Document shape:
- ID: `{source_id}_wi_{item_id}`
- Text starts with `Work Item {id}: {title}`
- Tags: `work_item_type`, `state`
- Properties: `id`, `title`, `url`

---

### Test Cases (`test-case`)

Fetches test case work items. XML tags are stripped from test steps.

| Config key | Description | Default |
|-----------|-------------|---------|
| `Query` | Custom WIQL query. | Fetches all Test Case work items |
| `Fields` | Comma-separated fields to fetch. | All fields |

Document shape:
- ID: `{source_id}_tc_{item_id}`
- Text starts with `Test Case {id}: {title}`; steps follow with XML stripped
- Tags: `automation_status`, `state`

---

### Test Results (`test-results`)

Fetches test run results including outcomes, error messages, and stack traces.

| Config key | Description | Default |
|-----------|-------------|---------|
| `LastNRuns` | Number of recent test runs to fetch. | `10` |
| `ResultsPerRun` | Maximum results to fetch per run. | `200` |

Document shape:
- ID: `{source_id}_tr_{run_id}_{result_id}`
- Text includes test name, run name, outcome, error message (if any), stack trace (if any)
- Tags: `outcome`, `run_name`

---

### Pull Requests (`pull-request`)

Fetches pull requests from a git repository.

| Config key | Description | Default |
|-----------|-------------|---------|
| `Repository` | Repository name. | — |
| `StatusFilter` | `active` \| `completed` \| `abandoned` \| `all` | `all` |
| `Top` | Maximum number of PRs to fetch. | `200` |

Document shape:
- ID: `{source_id}_pr_{pr_id}`
- Text includes title, description, author, branch (refs/heads/ prefix stripped), reviewers
- Tags: `status`, `author`
- Properties: `url` points to `{base_url}/_git/{repo}/pullrequest/{id}`

---

### Git Commits (`git-commits`)

Fetches commit history from a git repository.

| Config key | Description | Default |
|-----------|-------------|---------|
| `Repository` | Repository name. | — |
| `Branch` | Branch to fetch commits from. | `main` |
| `LastNCommits` | Number of recent commits to fetch. | `100` |

Document shape:
- ID: `{source_id}_commit_{short_id}` (first 8 chars of commit SHA)
- Text includes commit message, author, date, change counts (if available)
- Tags: `author`, `repository`

---

### Source Code (`code`)

Fetches source files from a git repository and parses them into code units (classes, methods, functions).

| Config key | Description | Default |
|-----------|-------------|---------|
| `Repository` | Repository name. | — |
| `Branch` | Branch to fetch from. | `main` |
| `GlobPatterns` | Comma-separated glob patterns, e.g. `**/*.cs, **/*.ts`. | `**/*.cs` |

Supported languages: C#, TypeScript, Go, PowerShell, Markdown. Unrecognised file types are indexed as plain text.

Document shape:
- ID: `{source_id}_{file_path}_{slug}` where slug is derived from the code unit name
- Text is the `enriched_text` format: namespace, kind, signature, language, file path, docs, then full source
- Tags: `language`, `kind`
- Properties: `title` (unit name), `file_path`, `repository`

---

### Documentation (`documentation`)

Fetches wiki pages from an ADO wiki, or accepts a file upload.

**ADO Wiki sub-option**

| Config key | Description | Default |
|-----------|-------------|---------|
| `WikiName` | Wiki name to target. Falls back to the first wiki if not found. | First wiki in project |
| `PathFilter` | Fetch only pages under this path. | `/` (all pages) |

Wiki pages are parsed by the Markdown parser into sections. Sub-pages are recursed automatically.

Document shape:
- ID: `{source_id}_{page_path}_{slug}`
- Text is the section's full text
- Tags: `wiki_name`, `section`

**Upload sub-option**

Equivalent to the Manual provider's file upload, indexed into the Documentation collection.

---

### Build Results (`pipeline-build`)

Fetches recent builds of a pipeline definition. For failed or partially succeeded builds, the task timeline is also fetched to surface which tasks failed.

| Config key | Description | Default |
|-----------|-------------|---------|
| `PipelineId` | Pipeline definition ID. | `0` (all pipelines) |
| `LastNBuilds` | Number of recent builds to fetch. | `5` |

Document shape:
- ID: `{source_id}_build_{build_id}`
- Text includes build number, result, pipeline ID, finish time, and failed task names (for failed/partiallySucceeded builds)
- Tags: `pipeline_id`, `build_result`, `status`

---

## Common patterns

### Targeting multiple repositories

Create one source per repository. Use the `source_name` filter on MCP search tools to limit results to a specific repository.

### On-premise TFS with Windows auth

Set `AuthType` to `ntlm` and set `BaseUrl` to your TFS collection URL, e.g. `https://tfs.company.com/DefaultCollection/MyProject`. Store your Windows password in an environment variable and reference it in `Password`.

### Private API with no standard auth

Use the Custom API provider with `AuthType: apikey`. Set `ApiKeyHeader` to the header name your API expects and `ApiKeyValue` to the name of an environment variable holding the key.
