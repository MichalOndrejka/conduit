# Dashboard

The **Dashboard** is Conduit's home page (`/`, also opened by clicking the logo; old `/stats` links redirect there). It shows what the agent's searches and the source syncs did since the Conduit process started.

## Where the data lives

Events are kept in memory only. Nothing is written to disk, so a restart (including a hot reload) starts with an empty dashboard.

## What is recorded

| Event | Recorded |
|---|---|
| Every MCP tool call | Tool, mode, latency, error. Search tools also record the query text, filters, page, collection and result count. `experience_retrieve` records the query and which entries came back. |
| Every sync of a source | Outcome (completed, failed with its phase, or cancelled); total, fetch, preprocess and embed time; documents and chunks; HTTP requests to the source's API and how many it refused for its rate limit, retries included. |

The process's first search embeds a query while the model is still loading, so it is marked *cold* and left out of latency figures.

## The page

- **Headline tiles:**
  - tool calls (failed calls flagged);
  - hits and misses: first-page searches that found something or nothing, each with its share of searches;
  - p95 semantic search latency;
  - syncs and API requests across every sync since the process started (failing sources and rate-limit hits flagged).
- **Sources:**
  - calls per tool and the empty-result share per search tool. Every registered tool is listed from the start, at zero until it is called;
  - the searches that found nothing, with their query text and filters;
  - API requests per provider (GitHub, Azure DevOps) across every sync since the process started, retries included, with rate-limit hits, to show how much load Conduit puts on each service;
  - sync time per source.
- **Experience:** entries remembered, memories recalled, recall misses, the most-recalled entries, and entries not recalled since the process started.

Every chart mark shows its values on hover and keyboard focus.
