# Example sources

## `seefood-sources.json`

Importable test sources for the **SeeFood** demo dataset in Azure DevOps
(`https://dev.azure.com/michalondrejkask/SeeFood`). Each is a generic HTTP
source pointing at a single ADO REST endpoint that returns a `value` array,
authenticated with a PAT via basic auth.

| Source | Type → collection | ADO endpoint |
|---|---|---|
| SeeFood API — Commits | `git-commits` | `git/repositories/seefood-api/commits` |
| SeeFood App — Commits | `git-commits` | `git/repositories/seefood-app/commits` |
| SeeFood — Work Items | `workitem` | `wit/workitems?ids=13…30` |
| SeeFood — Test Cases | `test-case` | `wit/workitems?ids=31…35` |
| SeeFood — Build Pipelines | `pipeline-build` | `build/definitions` |

### How to use

1. **Credentials** page → add a credential named exactly **`ado-pat`** holding a
   Personal Access Token for the `michalondrejkask` org. Scopes: *Code (Read)*,
   *Work Items (Read)*, *Build (Read)*.
2. **Sources** page → **Import** → choose this file.
3. **Sync** each source (or use **Save & sync** after editing).

All sources carry `Platform=ado` + `Ado*` metadata, so opening one re-fills the
**Azure DevOps** tab in the editor.

### Caveats (generic-runner limitations)

- **Commits** and **Build Pipelines** map cleanly — real titles (commit message /
  pipeline name), stable IDs, meaningful content.
- **Work Items** and **Test Cases**: ADO nests fields under a `fields` object and
  the generic runner does flat field lookups, so `ContentFields=fields` indexes
  the whole blob (the text *is* searchable) but titles render as "Item N". A
  dedicated ADO connector would flatten `System.Title` etc.
- **Build Pipelines** indexes pipeline *definitions* (ids 2/3), not build *runs* —
  runs need the hosted-parallelism grant.
- **Wiki** and **repo file contents** are intentionally omitted: they need
  multi-request/tree-walk flows the single-endpoint generic source can't express.
