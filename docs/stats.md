# Stats

The **Stats** page shows what the agent's searches and the source syncs did since the Conduit process started.

## Where the data lives

Events are kept in memory only. Nothing is written to disk, so a restart (including a hot reload) starts with empty stats.

## What is recorded

| Event | Recorded |
|---|---|
| Every MCP tool call | Tool, mode, latency, error. Search tools also record the query text, filters, page, collection and result count. `experience_retrieve` records the query and which entries came back. |
| Every sync of a source | Outcome (completed, failed with its phase, or cancelled); total, fetch, preprocess and embed time; documents and chunks; HTTP requests to the source's API and how many it refused for its rate limit, retries included. |

The process's first search embeds a query while the model is still loading, so it is marked *cold* and left out of latency figures.

## The page

- **Headline tiles:**
  - searches, and the share that found nothing;
  - further pages per search (the answer wasn't on page 1);
  - p95 search latency, sync time and API requests (with rate-limit hits flagged).
- **Search Quality:**
  - empty results per tool;
  - calls per tool;
  - the searches that found nothing, with their query text;
  - the filter values used and how often each came back empty. A value that always comes back empty usually doesn't exist in the data.
- **Syncs:**
  - API requests per provider (GitHub, Azure DevOps) across every sync since the process started, retries included, with rate-limit hits, to show how much load Conduit puts on each service;
  - sync time per source.
- **Experience:** recalls, recalls that found nothing, the most-recalled entries, and entries not recalled since the process started.

Every chart mark shows its values on hover and keyboard focus.
