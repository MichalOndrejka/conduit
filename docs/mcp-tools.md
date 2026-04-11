# MCP Tools Reference

Conduit exposes its search and memory capabilities as MCP tools. Claude can call these tools directly during a conversation.

## Connecting

Add to `claude_desktop_config.json`:
```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://localhost:5000/mcp"
    }
  }
}
```

Or in VS Code, use the `.vscode/mcp.json` already included in the repo.

---

## Search tools

All search tools share the same signature:

```
tool_name(query: str, top_k: int = 5, source_name: str | None = None) -> str
```

- **`query`** — Natural language query. The query is embedded and matched semantically.
- **`top_k`** — Number of results to return. Default `5`.
- **`source_name`** — Optional filter. When set, only documents from sources with that exact name are returned. Useful when multiple sources of the same type exist (e.g. two different repositories).

Results are returned as a JSON array. Each result contains:

```json
{
  "id": "source_id_wi_12345",
  "score": 0.87,
  "text": "Work Item 12345: Fix login timeout...",
  "tags": { "source_name": "My ADO Source", "state": "Active" },
  "properties": { "title": "Fix login timeout", "url": "https://..." }
}
```

### `search_documents`

Searches manually uploaded documents and pasted text.

Best for: design documents, ADRs, meeting notes, PDFs, one-off reference material.

### `search_workitems`

Searches work items — bugs, tasks, user stories, features, epics.

Best for: finding related issues, checking if a bug has been filed, understanding sprint scope.

### `search_code`

Searches source code at the code-unit level (classes, methods, functions).

Best for: finding implementations, understanding how a feature is built, locating where a concept is defined.

### `search_builds`

Searches pipeline build results including failure details.

Best for: diagnosing CI failures, understanding build history, finding which builds broke.

### `search_testcases`

Searches test case definitions including test steps.

Best for: finding existing test coverage, understanding expected behaviour, checking automation status.

### `search_documentation`

Searches wiki pages and documentation sections.

Best for: finding architectural decisions, process documentation, onboarding guides.

### `search_pullrequests`

Searches pull requests — titles, descriptions, branch names, reviewer lists.

Best for: understanding what changed and why, finding related PRs, reviewing who reviewed what.

### `search_test_results`

Searches test execution results — outcomes, error messages, stack traces.

Best for: finding flaky tests, diagnosing test failures, checking historical pass rates for a test.

### `search_commits`

Searches git commit history — messages, authors, file change summaries.

Best for: understanding when a change was made, finding the commit that introduced a behaviour, reviewing recent activity.

---

## Experience tools

### `retrieve_experience`

```
retrieve_experience(query: str, top_k: int = 5) -> str
```

Recalls relevant past experience: guidance, preferences, known mistakes, and past decisions. Returns a JSON object with an `experience` array of strings.

**Always call this at the start of every new task or user request.** The experience store is where Claude's memory of the project accumulates over time.

Example:
```
retrieve_experience("implementing authentication")
```

Returns:
```json
{
  "experience": [
    "For this project, always use the NTLM auth path for on-premise TFS...",
    "The team prefers feature branches prefixed with feat/..."
  ]
}
```

### `remember`

```
remember(situation: str, guidance: str) -> str
```

Stores information that should be recalled in future sessions.

- **`situation`** — Describes when this guidance applies. Be specific: "When writing C# unit tests in this repo" is better than "writing tests".
- **`guidance`** — The exact instruction or fact to recall. One clear, actionable statement per entry.

Call this proactively whenever you learn something the user would want enforced in future conversations — a preference, a constraint, a decision, or a lesson from a mistake.

Example:
```
remember(
  situation="Deploying to the staging environment",
  guidance="Always run the database migration script before deploying the API service. Missing this step caused an outage in March 2024."
)
```

Returns:
```json
{ "status": "stored", "entry_id": "abc123..." }
```

---

## Usage patterns

### Start of every task

```
1. retrieve_experience("<brief description of current task>")
2. Use search tools to gather relevant context
3. Complete the task
4. remember() any new guidance learned during the task
```

### Scoping a search to one source

If you have multiple ADO sources (e.g. two different repos), use `source_name` to limit results:

```
search_code("authentication middleware", source_name="Backend API")
```

### Combining tools for context

For a code change task, a useful sequence is:
1. `retrieve_experience` — check for relevant past decisions
2. `search_code` — find the implementation
3. `search_workitems` — find related requirements or bugs
4. `search_pullrequests` — check how similar changes were handled before
