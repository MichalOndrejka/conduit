# Accuracy evals

`conduit eval` measures how accurately documents are found under different ways of organizing sources into collections ("layouts"). It was built to decide, with numbers, whether merging the source types (Requirements and Test Cases into Work Items and Documentation, Test Code into Code) loses accuracy. Conduit now uses the merged layout (`merged_fields`). `current` is kept as the baseline it was measured against.

It needs the same Qdrant and embedding model Conduit uses. The agent level also needs Claude API credentials (`ANTHROPIC_API_KEY`, or an `ant auth login` profile). Eval collections are named `eval_*` and never touch Conduit's own `conduit_*` collections.

## Layouts

| Layout | Collections | How a question is narrowed |
|---|---|---|
| `current` (baseline) | One per source type, as before the merge | By picking the type's search tool |
| `merged_nofilter` | One per shape: work items, code, documentation | Not at all: the cost of merging when the agent doesn't filter |
| `merged_fields` (what Conduit ships) | Merged | By the company's own fields: work item types, tags, folders, and `code_kind` (test or production, derived from the file path) |
| `merged_category` | Merged | By a per-source category label |

## Two levels

**Retrieval level** (`conduit eval retrieval`, free): every question runs under every layout with the ideal tool and filters for it. Filters taken from the dataset's per-company `scopes` stand in for an agent that always finds the right ones. This isolates what the organization of collections does to ranking:
- what merging costs when nobody filters (`merged_nofilter`);
- what imperfect filters lose: `code_kind` path detection, a scope that misses some documents, or Qdrant's filtered search on real vectors.

With perfect filters a merged layout should match `current`, so a gap there points to a filter problem, not a layout problem.

**Agent level** (`conduit eval agent`, costs API money): Claude answers every question with each layout's tools.
- **`current`** gets the six type-specific tools from before the merge.
- **The merged layouts** get Conduit's real search tools: `search_workitem`, `search_code` and `search_documentation`.
  - **Same code as production:** their descriptions, request schema, filters, `facets` mode and empty-result facets all come from `internal/mcptools`, run over the eval index (`mcptools.RunSearchTool`).
  - **Shared facet counting:** facets are counted by the same `rag.Facets` the server uses.
  - **Kept in sync:** a change to the tools is measured without touching the eval. `merged_category` adds its `category` filter on top.

Claude ends with the IDs of the documents that answer the question. Each answer is scored on recall, precision, rank and tool calls. This is the level that shows whether an agent actually finds the filters the merged design relies on. Without `-yes` the command only prints how many agent runs it would make.

## Datasets

**`synthetic`** (committed, `internal/eval/datasets/synthetic.json`): three fictional companies that organize their data differently.

| Company | Setup | Requirements and test cases | Tests |
|---|---|---|---|
| Northwind | Azure DevOps, custom process | Work items (Product Requirement, Test Case) | C#, in a separate `tests/` project |
| Contoso | Azure DevOps Scrum | Markdown files in the repository | Go `*_test.go` beside the code |
| Fabrikam | GitHub, with issue types | Requirements are labelled Feature issues; test cases are files | TypeScript `*.spec.ts` beside the code |

Each topic has one document of every kind, so a requirement, its test case, the related bug, the code, its test and the docs all compete for the same question. Edit the generator `internal/eval/datasets/gen_synthetic.py` and re-run it rather than editing the JSON by hand.

**Your own data** (private, never committed): `eval-private/` and `eval-results/` are ignored by git.

```sh
conduit eval export -out eval-private/dataset.json     # fetches every enabled source, as a sync would
conduit eval find -dataset eval-private/dataset.json -q "password reset expiry"   # look up document IDs
# write eval-private/queries.json (format below), then:
conduit eval retrieval -dataset eval-private/dataset.json -queries eval-private/queries.json
```

A queries file adds labelled questions and, for `merged_fields`, the scopes describing how your company's data is narrowed: the work item types or folders of its requirements and test cases. `export` writes the filters a sync would (`source`, `provider`, `code_kind`), so a Code source's tests are `test_code` documents; requirements and test cases are only told apart by your scopes and labels.

```json
{
  "companies": [{
    "id": "real",
    "scopes": {
      "requirement": [{ "collection": "documentation", "filters": { "path": ["docs/requirements"] } }]
    },
    "queries": [
      { "id": "q1", "intent": "requirement", "text": "How long is a password reset link valid?", "relevant": ["<source_doc_id>"] }
    ]
  }]
}
```

Intents: `requirement`, `test_case`, `work_item`, `code`, `test_code`, `documentation`, `commit`, `pipeline_run`. Collections in scopes: `work_items`, `code`, `documentation`, `commits`, `pipeline_runs`.

## Running

```sh
conduit eval retrieval                                   # synthetic dataset, all layouts
conduit eval retrieval -reuse                            # skip re-embedding an unchanged corpus
conduit eval agent -query-ids nw-q1,ct-q1,fb-q1          # prints the plan; add -yes to run
conduit eval agent -yes -reuse -effort medium
```

Each run writes `eval-results/<kind>-<timestamp>.md` and `.json`. The JSON has every question's ranking and, for agent runs, every tool call. The Markdown report shows:
- overall scores against the baseline;
- breakdowns by intent and by company;
- every question whose answer moved, with what ranked above it.

## Reading the numbers

- **Hit@1 / MRR:** whether the answer is the top result / how high it ranks. These matter most, because agents read the first page.
- **Recall@5 / Recall@K:** whether the answer is on the first page (Conduit pages by 5) / anywhere in the top K.
- **Empty:** nothing came back at all, which an agent can't tell apart from "no such data".
- **Agent level:**
  - **Answer recall / precision:** whether the agent named the right documents, and only those.
  - **Seen by searches:** the answer appeared in some tool result, whether or not the agent picked it.
  - **Tool calls:** the effort it took.

A merge is safe when the merged layout's agent-level answer recall and Hit@1 match the baseline, and it needs at most about one extra tool call (the `facets` lookup).
