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

`semantic_search` and `pattern_search` return one match per call: `{ "results": [...], "page": N, "has_more": bool }`. `has_more: true` means call again with `page + 1`. When `results` is empty, a `note` explains why (nothing embedded yet, no match, or the last page was already reached).

Every result carries `chunk_index`/`total_chunks`/`has_previous`/`has_next`. If a match looks cut off mid-thought, use `retrieve_chunk` with the same `source_doc_id` and `chunk_index ± 1` to fetch the adjacent text deterministically — no new search needed.

```json
{
  "results": [
    {
      "id": "source_id_wi_12345",
      "score": 0.87,
      "text": "Work Item 12345: Fix login timeout...",
      "tags": { "source_name": "My ADO Source", "state": "Active" },
      "properties": { "title": "Fix login timeout", "url": "https://..." },
      "source_doc_id": "source_id_wi_12345",
      "chunk_index": 0,
      "total_chunks": 3,
      "has_previous": false,
      "has_next": true
    }
  ],
  "page": 1,
  "has_more": true
}
```

### `mode: "semantic_search"` — ranked, natural-language search

- **`query`** (required) — natural-language query, embedded and matched by similarity.
- **`page`** (required) — rank to return, starting at `1` (most relevant).
- **`source_name`** (optional) — restrict results to one source.

### `mode: "retrieve_chunk"` — deterministic fetch by ID

- **`source_doc_id`**, **`chunk_index`** (both required) — copy from a prior result, adjusting `chunk_index` by ±1 to walk to the neighboring chunk.

No embedding call is made — it's a direct point lookup, so it always returns the exact chunk (or none, with a `note`) regardless of relevance:

```json
{ "results": [ { "text": "...continuation of the previous chunk...", "source_doc_id": "source_id_wi_12345", "chunk_index": 1, "total_chunks": 3, "has_previous": true, "has_next": true } ] }
```

### `mode: "pattern_search"` — exact literal/regex match

- **`pattern`** (required) — literal substring, or (with `regex: true`) a Go RE2 regular expression.
- **`page`** (required) — match to return, starting at `1`, in storage order (not ranked).
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
