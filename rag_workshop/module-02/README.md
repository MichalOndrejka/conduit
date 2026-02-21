# Module 2 — Similarity Search

> **~15 min** · Builds on [Module 1](../module-01/README.md)

## Learning objective

By the end of this module you will have:

- Understood **cosine similarity** and what search scores mean
- Used `POST /search/topk` to find documents similar to a query
- Observed how results are ranked by semantic relevance, not keyword matching

---

## Concepts introduced

### Cosine similarity

When you search, the API embeds your query text into a vector (just like indexing), then compares it against every stored vector using **cosine similarity**.

Cosine similarity measures the angle between two vectors:
- **1.0** = identical meaning (vectors point in the same direction)
- **0.0** = completely unrelated
- Values typically range **0.3 – 0.9** for real-world text

**Key insight:** This is *semantic* search, not keyword search. The query "How do plants make food?" will match a document about "photosynthesis" even though the words don't overlap — because their *meanings* are close in vector space.

### Top-K search

`POST /search/topk` returns exactly **K** results, ranked by cosine similarity (highest first). If K=3, you get the 3 most similar documents, regardless of how similar they actually are.

Use Top-K when you want a **fixed number of results** — like "show me the 5 most relevant documents."

---

## What changed from Module 1

| New file | Purpose |
|----------|---------|
| `Extensions/QdrantPayloadExtensions.cs` | Converts gRPC payload to clean JSON for API responses |
| `Endpoints/SearchEndpoints.cs` | `POST /search/topk` — embed query → search Qdrant → return ranked hits |

| Changed file | What changed |
|-------------|-------------|
| `Models/Requests.cs` | Added `TopKSearchRequest` and `SearchHit` records |
| `Program.cs` | Added `MapSearchEndpoints(collectionName)` |

### Code walkthrough

#### Search request & result models — [`Requests.cs`](src/Qdrant.Demo.Api/Models/Requests.cs)

Two new records power the search API:

```csharp
public record TopKSearchRequest(
    string QueryText,
    int K = 5,
    Dictionary<string, string>? Tags = null
);

public record SearchHit(
    string? Id,
    float Score,
    Dictionary<string, object?> Payload
);
```

`TopKSearchRequest` takes the free-text query and the number of results to return. `Tags` is declared here but won't be used until Module 4 (filtered search). `SearchHit` carries the point id, its cosine similarity score, and the full payload.

#### The search endpoint — [`SearchEndpoints.cs`](src/Qdrant.Demo.Api/Endpoints/SearchEndpoints.cs)

The endpoint embeds the query text into a vector (same model used for indexing), then asks Qdrant for the closest matches:

```csharp
app.MapPost("/search/topk", async (
    [FromBody] TopKSearchRequest req,
    QdrantClient qdrant,
    IEmbeddingService embeddings,
    CancellationToken ct) =>
{
    var vector = await embeddings.EmbedAsync(req.QueryText, ct);

    var hits = await qdrant.SearchAsync(
        collectionName: collectionName,
        vector: vector,
        limit: (ulong)req.K,
        payloadSelector: true,
        cancellationToken: ct);

    return Results.Ok(hits.ToFormattedHits());
});
```

`payloadSelector: true` tells Qdrant to include the stored payload (text, timestamp, etc.) in each result — without it you'd only get ids and scores.

#### Converting gRPC payloads — [`QdrantPayloadExtensions.cs`](src/Qdrant.Demo.Api/Extensions/QdrantPayloadExtensions.cs)

Qdrant returns results as gRPC `ScoredPoint` objects with a protobuf-typed `Value` payload. This extension method converts them into clean `SearchHit` records with a regular `Dictionary<string, object?>` so the API returns human-readable JSON:

```csharp
public static IEnumerable<SearchHit> ToFormattedHits(
    this IReadOnlyList<ScoredPoint> hits)
{
    return hits.Select(h => new SearchHit(
        Id: h.Id?.Uuid ?? h.Id?.Num.ToString(),
        Score: h.Score,
        Payload: h.Payload.ToDictionary()
    ));
}
```

The `ToDictionary()` helper recursively walks the protobuf `Value` tree (strings, doubles, integers, bools, nested structs, lists) and produces plain CLR objects.

---

## Step 1 — Start Qdrant and run the API

```bash
cd module-02
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

## Step 2 — Index some documents

If you haven't already, index the sample articles using **Swagger UI**:

1. Open **http://localhost:8080/swagger** in your browser
2. Find the **POST /documents** endpoint and click it to expand
3. Click **Try it out**
4. Paste each JSON body below and click **Execute** (repeat for all three):

**Document 1:**

```json
{
  "id": "article-001",
  "text": "Photosynthesis is the process by which green plants convert sunlight into chemical energy, producing oxygen as a byproduct."
}
```

**Document 2:**

```json
{
  "id": "article-002",
  "text": "Quantum entanglement is a phenomenon where two particles become linked, so the quantum state of one instantly influences the other, regardless of distance."
}
```

**Document 3:**

```json
{
  "id": "article-003",
  "text": "Machine learning is a subset of artificial intelligence where algorithms learn patterns from data rather than being explicitly programmed."
}
```

## Step 3 — Search!

In **Swagger UI**, find the **POST /search/topk** endpoint, click **Try it out**, paste the following body and click **Execute**:

```json
{
  "queryText": "How do plants produce energy from sunlight?",
  "k": 3
}
```

In the **Response body** you should see all 3 documents ranked by similarity:

```json
[
  {
    "id": "99561fa9-ef52-df51-bf9c-469607d4596e",
    "score": 0.64333606,
    "payload": {
      "indexed_at_ms": 1770831019588,
      "text": "Photosynthesis is the process by which green plants convert sunlight into chemical energy, producing oxygen as a byproduct."
    }
  },
  {
    "id": "df45c345-85a8-6a5e-aa63-4bbf79fe3f21",
    "score": 0.0954015,
    "payload": {
      "indexed_at_ms": 1770831028172,
      "text": "Machine learning is a subset of artificial intelligence where algorithms learn patterns from data rather than being explicitly programmed."
    }
  },
  {
    "id": "a6ea9a4f-e9a6-5c50-b678-3c016e58f96b",
    "score": 0.093406975,
    "payload": {
      "text": "Quantum entanglement is a phenomenon where two particles become linked, so the quantum state of one instantly influences the other, regardless of distance.",
      "indexed_at_ms": 1770831023087
    }
  }
]
```

**Notice:** The photosynthesis article scores highest (~0.64) because it's semantically closest to the query. The other articles score much lower because they're about different topics. This is cosine similarity in action.

---

## Exercises

### Exercise 2.1 — Try different queries

Using **POST /search/topk** in Swagger UI, try each query below and observe which document scores highest:

**Query 1** — should match article-002 (quantum physics):

```json
{
  "queryText": "spooky action at a distance",
  "k": 3
}
```

**Query 2** — should match article-003 (ML/AI):

```json
{
  "queryText": "training algorithms on datasets",
  "k": 3
}
```

### Exercise 2.2 — Change K

In Swagger UI, try the same search with `"k": 1` to get only the single best match. Then try `"k": 10` — with only 3 documents indexed, you'll still get at most 3 results.

### Exercise 2.3 — Search for something unrelated

Using **POST /search/topk** in Swagger UI, try:

```json
{
  "queryText": "best pizza recipe",
  "k": 3
}
```

You'll still get 3 results (Top-K always returns K results if the collection has enough), but the scores will be noticeably lower. There's no "minimum relevance" filter yet — that comes in Module 4 (threshold search).

---

## ✅ Checkpoint

At this point you have:

- [x] A working `POST /search/topk` endpoint
- [x] Observed semantic search in action — meaning-based, not keyword-based
- [x] Understood cosine similarity scores
- [x] Understanding of: Top-K search, score interpretation, QdrantPayloadExtensions

## 🧹 Clean Up

Before moving to the next module, stop everything started in this module:

1. **Stop the local API** — press `Ctrl+C` in the terminal where `dotnet run` is running
2. **Stop Docker containers** — from the `module-02` directory:

```bash
docker compose down
```

This stops Qdrant so the next module starts fresh.

**Next →** [Module 3 — Metadata](../module-03/README.md)
