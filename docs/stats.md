# Stats

The **Stats** page shows what the agent's searches and the source syncs did, per **build**, so versions can be compared over time: did a change make searches come back empty more often, or make a sync slower or more expensive?

## What a build is

A build is the code a Conduit process runs: the git commit plus a hash of any uncommitted changes, read from the git checkout at startup.
- **Same code, same build:** restarting the same code (`go run`, an air hot reload, a container restart) stays in one build.
- **Changed code, new build:** any committed or uncommitted change starts a new one.
- **Ignored:** untracked tool folders such as `.venv` or editor caches don't count as changes.
- **Fallbacks:** without a git checkout, Conduit uses the VCS stamp that `go build` embeds, then the version.

## Where the history lives

Events are appended to `conduit-stats.jsonl` in the data directory (next to `conduit-sources.json`), one JSON line per event, written as it happens. Nothing is kept in memory, so a process that lives only minutes loses nothing. The file is ignored by git.

At startup the history is trimmed to the newest **30 builds**, dropping older builds whole so every kept build stays complete. Set `CONDUIT_STATS_KEEP_BUILDS` to change the number.

## What is recorded

| Event | Recorded |
|---|---|
| Every MCP tool call | Tool, mode, latency, error. Search tools also record the query text, filters, page, collection and result count. `experience_retrieve` records the query and which entries came back. |
| Every sync of a source | Outcome (completed, failed with its phase, or cancelled); total, fetch, preprocess and embed time; documents and chunks; HTTP requests to the source's API and how many it refused for its rate limit, retries included. |

The process's first search embeds a query while the model is still loading, so it is marked *cold* and left out of latency figures.

## The page

- **Filter row:** pick the build to show and the one to compare with (default: the newest build with activity and the build before it). Everything below follows that choice.
- **Headline tiles:**
  - searches, and the share that found nothing;
  - further pages per search (the answer wasn't on page 1);
  - p95 search latency, sync time and API requests.
  
  Each tile shows the change against the compared build and a sparkline across builds.
- **Search Quality:**
  - trends across builds for empty results, further pages, chunk walks (`retrieve_chunk`, a cut-off answer) and latency;
  - empty results per tool, before and after;
  - calls per tool;
  - the searches that found nothing, with their query text;
  - the filter values used and how often each came back empty. A value that always comes back empty usually doesn't exist in the data.
- **Syncs:**
  - sync time per build, split into fetch and embed;
  - API requests, rate-limit hits and failed syncs per build;
  - sync time per source, before and after;
  - the per-source table, with flagged drops in documents or chunks (usually a broken token or filter).
- **Experience:** recalls, recalls that found nothing, the most-recalled entries, and entries no kept build has recalled.
- **Builds:** the exact numbers behind every chart.

Every chart mark shows its values on hover and keyboard focus.
