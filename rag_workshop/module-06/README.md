# Module 6 — Advanced Chat

> **~15 min** · Builds on [Module 5](../module-05/README.md)

## Learning objective

By the end of this module you will have:

- Made the system prompt **customizable** per request
- Added **tag filtering** to the chat retrieval step
- Added a **score threshold** to exclude low-relevance context
- Understood how these controls affect answer quality

---

## Concepts introduced

### Custom system prompts

In Module 5 the system prompt was hard-coded. Now the caller can supply their own via the `systemPrompt` field. This lets the same endpoint serve different personas:

- A legal assistant: *"You are a legal advisor. Cite applicable statutes."*
- A customer support bot: *"Answer in a friendly tone. Suggest help articles."*
- A code reviewer: *"Review the code and suggest improvements."*

When `systemPrompt` is omitted, the default ("answer only from context") is used.

### Filtered retrieval

The `tags` field narrows which documents are retrieved as context. If you have thousands of documents across many categories, filtering ensures the LLM only sees relevant ones:

```json
{
  "question": "How does DNA replication work?",
  "tags": { "category": "biology" }
}
```

### Score threshold

The `scoreThreshold` field excludes low-similarity documents from the context window, even if they'd be in the top-K. This prevents the LLM from being distracted by irrelevant noise:

```json
{
  "question": "What is photosynthesis?",
  "k": 10,
  "scoreThreshold": 0.5
}
```

---

## What changed from Module 5

| Changed file | What changed |
|-------------|-------------|
| `Models/Requests.cs` | `ChatRequest` now has `ScoreThreshold`, `Tags`, and `SystemPrompt` parameters |
| `Endpoints/ChatEndpoints.cs` | Injects `IQdrantFilterFactory`; applies `req.Tags` and `req.ScoreThreshold` to search; uses `req.SystemPrompt ?? DefaultSystemPrompt` |

No new files — this is a refinement of the existing chat endpoint.

### Code walkthrough

#### Extended chat request — [`Requests.cs`](src/Qdrant.Demo.Api/Models/Requests.cs)

The `ChatRequest` record gains three new optional parameters:

```csharp
public record ChatRequest(
    string Question,
    int K = 5,
    float? ScoreThreshold = null,
    Dictionary<string, string>? Tags = null,
    string? SystemPrompt = null
);
```

`ScoreThreshold` excludes low-relevance documents, `Tags` filters by metadata, and `SystemPrompt` lets the caller set the LLM's persona. All are optional — existing callers continue to work unchanged.

#### Applying the controls — [`ChatEndpoints.cs`](src/Qdrant.Demo.Api/Endpoints/ChatEndpoints.cs)

The retrieval step now incorporates all three controls:

```csharp
// 2. Retrieve the top-K most similar documents
var filter = filters.CreateGrpcFilter(req.Tags);

var hits = await qdrant.SearchAsync(
    collectionName: collectionName,
    vector: vector,
    limit: (ulong)req.K,
    filter: filter,
    scoreThreshold: req.ScoreThreshold,
    payloadSelector: true,
    cancellationToken: ct);
```

And the system prompt is now caller-controlled with a sensible fallback:

```csharp
var systemPrompt = req.SystemPrompt ?? DefaultSystemPrompt;

List<ChatMessage> messages =
[
    new ChatMessage(ChatRole.System, systemPrompt),
    new ChatMessage(ChatRole.User,
        $"""
        Context:
        {context}

        Question: {req.Question}
        """)
];
```

The same `DefaultSystemPrompt` from Module 5 is used when the caller doesn't provide one — backward compatible by design.

---

## Step 1 — Start Qdrant and run the API

```bash
cd module-06
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

## Step 2 — Index documents (if not already present)

1. Open **Swagger UI** in your browser: **http://localhost:8080/swagger**
2. Find the **POST /documents** endpoint, click **Try it out**
3. Paste each JSON body below and click **Execute**:

**Document 1:**

```json
{
  "id": "bio-001",
  "text": "Photosynthesis converts sunlight into chemical energy in green plants.",
  "tags": { "category": "biology" }
}
```

**Document 2:**

```json
{
  "id": "phys-001",
  "text": "Quantum entanglement links two particles across any distance.",
  "tags": { "category": "physics" }
}
```

## Step 3 — Custom system prompt

In **Swagger UI**, find the **POST /chat** endpoint, click **Try it out**, paste the following body and click **Execute**:

```json
{
  "question": "Tell me about photosynthesis",
  "systemPrompt": "You are a children's science teacher. Explain everything as if speaking to a 7-year-old. Use simple words and fun analogies."
}
```

Compare this with the default prompt — same question, very different tone.

## Step 4 — Filtered chat

Using **POST /chat** in Swagger UI, only pull from biology documents:

```json
{
  "question": "Tell me something interesting",
  "tags": { "category": "biology" }
}
```

The physics document is never seen by the LLM.

## Step 5 — Score threshold

Using **POST /chat** in Swagger UI, exclude weak matches:

```json
{
  "question": "best pizza recipe",
  "scoreThreshold": 0.6
}
```

With a 0.6 threshold, no documents about biology/physics should pass. The LLM will respond that it has no relevant context.

---

## Exercises

### Exercise 6.1 — Persona switch

Call `/chat` three times with the same question but different system prompts: a formal academic, a pirate, and a haiku poet. Observe how the tone changes while the factual content stays the same.

### Exercise 6.2 — Combine all controls

Using **POST /chat** in Swagger UI, try combining all parameters:

```json
{
  "question": "How do plants get energy?",
  "k": 3,
  "scoreThreshold": 0.4,
  "tags": { "category": "biology" },
  "systemPrompt": "Answer in exactly one sentence."
}
```

---

## ✅ Checkpoint

At this point you have:

- [x] Customizable system prompts
- [x] Tag-filtered retrieval in the chat endpoint
- [x] Score threshold to exclude weak matches
- [x] Understanding of: persona control, filtered RAG, context quality tuning

## 🧹 Clean Up

Before moving to the next module, stop everything started in this module:

1. **Stop the local API** — press `Ctrl+C` in the terminal where `dotnet run` is running
2. **Stop Docker containers** — from the `module-06` directory:

```bash
docker compose down
```

This stops Qdrant so the next module starts fresh.

**Next →** [Module 7 — Chunking](../module-07/README.md)
