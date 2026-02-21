# Module 3 — Metadata

> **~10 min** · Builds on [Module 2](../module-02/README.md)

## Learning objective

By the end of this module you will have:

- Understood the difference between **tags** (filterable) and **properties** (informational)
- Stored structured metadata alongside document vectors
- Seen how the `tag_` / `prop_` prefix convention keeps the payload organized

---

## Concepts introduced

### Tags vs Properties

When you index a document you can now attach two kinds of metadata:

| | Tags | Properties |
|---|---|---|
| **Payload prefix** | `tag_{key}` | `prop_{key}` |
| **Purpose** | Filtering during search | Returned with results for display |
| **Example** | `"category": "science"` | `"source_url": "https://..."` |
| **Indexed by Qdrant?** | Yes — used in `must` / `should` filter clauses | No — stored but not used for filtering |

**Why the prefix?** Qdrant stores all metadata in a flat key-value payload. Prefixing tags with `tag_` and properties with `prop_` avoids collisions and makes it easy for the filter factory (coming in Module 4) to build filter clauses automatically.

### Example payload after indexing

```json
{
  "text": "Photosynthesis converts sunlight into chemical energy...",
  "indexed_at_ms": 1718500000000,
  "tag_category": "biology",
  "tag_level": "introductory",
  "prop_source_url": "https://example.com/biology/photosynthesis",
  "prop_author": "Dr. Green"
}
```

---

## What changed from Module 2

| Changed file | What changed |
|-------------|-------------|
| `Models/PayloadKeys.cs` | Added `TagPrefix` and `PropertyPrefix` constants |
| `Services/DocumentIndexer.cs` | Stores tags and properties as prefixed payload fields |

No new endpoints — the existing `POST /documents` already accepts `tags` and `properties` in the request body (they were optional fields since Module 1). Now they are actually **stored**.

### Code walkthrough

#### The request model — [`Requests.cs`](src/Qdrant.Demo.Api/Models/Requests.cs)

The `DocumentUpsertRequest` record already declared `Tags` and `Properties` as optional parameters in earlier modules. This module doesn't change the record — it just starts *using* them:

```csharp
public record DocumentUpsertRequest(
    string? Id,
    string Text,
    Dictionary<string, string>? Tags = null,
    Dictionary<string, string>? Properties = null
);
```

Both are `Dictionary<string, string>?` — simple key-value pairs the caller can attach to any document.

#### Prefix constants — [`PayloadKeys.cs`](src/Qdrant.Demo.Api/Models/PayloadKeys.cs)

To keep the flat Qdrant payload organized, every tag key is prefixed with `tag_` and every property key with `prop_`:

```csharp
public static class PayloadKeys
{
    public const string Text        = "text";
    public const string IndexedAtMs = "indexed_at_ms";
    public const string TagPrefix      = "tag_";
    public const string PropertyPrefix = "prop_";
}
```

These constants are imported with `using static` in the indexer, so the code reads naturally (e.g. `$"{TagPrefix}{key}"`).

#### Storing metadata — [`DocumentIndexer.cs`](src/Qdrant.Demo.Api/Services/DocumentIndexer.cs)

After building the base payload (`text` + `indexed_at_ms`), the indexer loops over any supplied tags and properties and writes them into the Qdrant point's payload with the appropriate prefix:

```csharp
// Store tags as tag_{key} — these are indexed and filterable.
if (request.Tags is not null)
{
    foreach (var (key, value) in request.Tags)
        point.Payload[$"{TagPrefix}{key}"] = value;
}

// Store properties as prop_{key} — informational, not indexed.
if (request.Properties is not null)
{
    foreach (var (key, value) in request.Properties)
        point.Payload[$"{PropertyPrefix}{key}"] = value;
}
```

The `if` guard means existing callers that don't send tags or properties are unaffected — backward compatible by design.

---

## Step 1 — Start Qdrant and run the API

```bash
cd module-03
docker compose up -d    # starts Qdrant (http://localhost:6333)
```

Then run the API locally:

```bash
cd src/Qdrant.Demo.Api
```

```powershell
# PowerShell
$env:ASPNETCORE_URLS = "http://localhost:8080"
```

```bash
# Linux/macOS
export ASPNETCORE_URLS="http://localhost:8080"
```

```bash
dotnet run
```

## Step 2 — Index a document with metadata

1. Open **Swagger UI** in your browser: **http://localhost:8080/swagger**
2. Find the **POST /documents** endpoint, click **Try it out**
3. Paste the following JSON body and click **Execute**:

```json
{
  "id": "bio-001",
  "text": "Photosynthesis is the process by which green plants convert sunlight into chemical energy.",
  "tags": { "category": "biology", "level": "introductory" },
  "properties": { "source_url": "https://example.com/bio", "author": "Dr. Green" }
}
```

## Step 3 — Verify the metadata was stored

In **Swagger UI**, find the **POST /search/topk** endpoint, click **Try it out**, paste the following body and click **Execute**:

```json
{
  "queryText": "photosynthesis",
  "k": 1
}
```

In the response payload you should now see:

```json
[
  {
    "id": "6b6492a2-38cb-3f55-a58f-47956db480ee",
    "score": 0.5855994,
    "payload": {
      "indexed_at_ms": 1770836017589,
      "prop_source_url": "https://example.com/bio",
      "tag_level": "introductory",
      "prop_author": "Dr. Green",
      "text": "Photosynthesis is the process by which green plants convert sunlight into chemical energy.",
      "tag_category": "biology"
    }
  }
]
```

The tags and properties are stored alongside the vector — ready for filtering in the next module.

---

## Exercises

### Exercise 3.1 — Index several documents with different tags

Using **POST /documents** in Swagger UI, index these two documents (paste each body and click **Execute**):

**Document 1:**

```json
{
  "id": "phys-001",
  "text": "Quantum entanglement links two particles so the state of one instantly affects the other.",
  "tags": { "category": "physics", "level": "advanced" },
  "properties": { "source_url": "https://example.com/physics" }
}
```

**Document 2:**

```json
{
  "id": "cs-001",
  "text": "Machine learning algorithms learn patterns from data without being explicitly programmed.",
  "tags": { "category": "computer-science", "level": "intermediate" }
}
```

### Exercise 3.2 — Observe tags in search results

Using **POST /search/topk** in Swagger UI, search with:

```json
{
  "queryText": "particles linked together",
  "k": 3
}
```

The physics document should rank highest — and you should see its `tag_category` = `"physics"` in the payload.

### Exercise 3.3 — Think ahead

Right now tags are stored but search still returns all documents ranked by similarity. In the next module you'll add **filters** so you can say "search only biology documents" or "only introductory level" — and Qdrant will narrow the candidate set *before* computing similarity.

---

## ✅ Checkpoint

At this point you have:

- [x] Tags stored as `tag_{key}` and properties stored as `prop_{key}` in Qdrant
- [x] Documents enriched with structured metadata
- [x] Understanding of: Tags (filterable) vs Properties (informational), prefix convention

## 🧹 Clean Up

Before moving to the next module, stop everything started in this module:

1. **Stop the local API** — press `Ctrl+C` in the terminal where `dotnet run` is running
2. **Stop Docker containers** — from the `module-03` directory:

```bash
docker compose down
```

This stops Qdrant so the next module starts fresh.

**Next →** [Module 4 — Filtered Search](../module-04/README.md)
