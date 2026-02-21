# Qdrant.Demo — RAG Workshop

Ever wondered how AI chatbots answer questions using *your* data instead of making things up? That's **Retrieval-Augmented Generation (RAG)** and this workshop teaches you how to build one from scratch.

In about **3 hours**, you'll go from an empty .NET API to a fully working RAG system: store documents as vector embeddings in **Qdrant**, search them by meaning (not just keywords), and let **OpenAI** generate answers grounded in real content, with zero hallucination. Each of the 10 modules introduces exactly one concept (indexing, semantic search, metadata filtering, chat, text chunking, batch operations), so you learn by building, one step at a time.

**Stack:** .NET 10 · Qdrant · OpenAI · Docker

```mermaid
flowchart LR
    T["Documents + metadata"] -->|embed| EMB["OpenAI Embeddings"]
    EMB -->|vectors| QD[("Qdrant")]
    UQ["User Query"] -->|embed| EMB
    EMB -->|search| QD
    QD -->|top results| LLM["OpenAI Chat"]
    UQ -.->|question| LLM
    LLM --> A["Grounded Answer"]
```

---

## Workshop modules

Each module is a **self-contained, fully buildable** .NET project in its own folder. Modules build on each other — start at Module 0 and work your way down.

| # | Module | What you'll learn | ~Time |
|---|--------|-------------------|-------|
| **0** | [Setup & Orientation](module-00/README.md) | Docker, Qdrant Dashboard, Swagger UI, project skeleton | ~15 min |
| **1** | [Your First Document](module-01/README.md) | Embeddings, Qdrant points, `POST /documents`, deterministic IDs | ~15 min |
| **2** | [Similarity Search](module-02/README.md) | Cosine similarity, `POST /search/topk`, score interpretation | ~15 min |
| **3** | [Metadata](module-03/README.md) | Tags vs Properties, `tag_*` / `prop_*` payload prefixes | ~10 min |
| **4** | [Filtered Search](module-04/README.md) | Tag filters, threshold search, metadata-only search, payload indexes | ~20 min |
| **5** | [RAG Chat](module-05/README.md) | The full RAG pipeline, `POST /chat`, system prompt, hallucination guardrail | ~20 min |
| **6** | [Advanced Chat](module-06/README.md) | Custom system prompts, filtered chat, prompt engineering | ~15 min |
| **7** | [Chunking Long Documents](module-07/README.md) | Token limits, text chunking, overlap, sentence boundaries | ~25 min |
| **8** | [Batch Operations](module-08/README.md) | `POST /documents/batch`, error handling, bulk ingestion | ~10 min |
| **9** | [Chat UI](module-09/README.md) | Static file serving, single-file frontend, visual RAG experience | ~20 min |

> **Total workshop time:** ~3 hours at a comfortable pace.

The [`completed/`](completed/) folder contains the final state with **all** features for quick reference.

---

## Prerequisites

| Tool | Version | Why |
|------|---------|-----|
| [Docker Desktop](https://www.docker.com/products/docker-desktop/) | 4.x+ | Runs the Qdrant vector database ([free alternatives](materials/docker-alternatives.md)) |
| [.NET 10 SDK](https://dotnet.microsoft.com/download/dotnet/10.0) | 10.0+ | Build & run the API locally |
| [OpenAI API key](https://platform.openai.com/api-keys) | -- | Embeddings (`text-embedding-3-small`) and chat (`gpt-4o-mini`) |
| `curl` or similar HTTP client | -- | Test the API endpoints |

> **Cost note:** The workshop uses OpenAI's cheapest models. Expected cost is well under $1 per participant for the full workshop.

> **Running a workshop?** Send the [pre-workshop email](materials/pre-workshop-email.md) to participants ~1 week before. It lists everything they need to install ahead of time. This setup is **corporate-network friendly** -- .NET uses the Windows certificate store, so corporate SSL inspection proxies work transparently.

---

## Quick start

```bash
# 1. Clone and enter the repo
git clone https://github.com/PeterMilovcik/Qdrant.Demo.git
cd Qdrant.Demo

# 2. (Optional) Use a fixed local API port
# PowerShell:
$env:ASPNETCORE_URLS = "http://localhost:8080"
# Linux/macOS:
export ASPNETCORE_URLS="http://localhost:8080"

# 3. Start with Module 0
cd module-00
```

Then follow the [Module 0 README](module-00/README.md) to get started.

---

## Repository structure

```
Qdrant.Demo/
  README.md                         ← you are here
  global.json
  materials/                        ← Pre-workshop email template & setup instructions
  module-00/                        ← Setup & Orientation
  module-01/                        ← Your First Document
  module-02/                        ← Similarity Search
  module-03/                        ← Metadata
  module-04/                        ← Filtered Search
  module-05/                        ← RAG Chat
  module-06/                        ← Advanced Chat
  module-07/                        ← Chunking Long Documents
  module-08/                        ← Batch Operations
  module-09/                        ← Chat UI
  completed/                        ← Full reference implementation
```

Each module folder contains:

```
module-XX/
  README.md                         ← Module instructions & exercises
  Qdrant.Demo.sln                   ← Solution file
  global.json                       ← .NET SDK version pin
  docker-compose.yml                ← Qdrant vector database
  src/
    Qdrant.Demo.Api/                ← API code for this module
  tests/
    Qdrant.Demo.Api.Tests/          ← Unit tests for this module
```

---

## What is RAG?

**Retrieval-Augmented Generation** is a pattern where you:

1. **Index** — turn your documents into vectors (embeddings) and store them in a vector database.
2. **Retrieve** — given a user query, embed it and find the most similar documents.
3. **Generate** — feed the retrieved documents into an LLM to produce a grounded answer.

This workshop implements **all three steps** across the modules above.

---

## Appendix A — Glossary

| Term | Definition |
|------|------------|
| **Embedding** | A fixed-length array of floats that represents the semantic meaning of text. Similar texts produce similar vectors. |
| **Vector** | A list of numbers (e.g. 1536 floats). In this context, it is the embedding of a piece of text. |
| **Cosine similarity** | A measure of how similar two vectors are, ranging from 0 (unrelated) to 1 (identical meaning). |
| **Collection** | A Qdrant container that holds vectors of the same dimensionality — analogous to a database table. |
| **Point** | A single entry in a Qdrant collection: a unique id + a vector + an optional payload (metadata). |
| **Payload** | Arbitrary key/value metadata stored alongside a vector in Qdrant. Can be indexed for filtering. |
| **Upsert** | Insert-or-update: if the point-id already exists the point is overwritten, otherwise it is created. |
| **Grounding** | Providing an LLM with factual context (retrieved documents) so it answers based on evidence, not imagination. |
| **Hallucination** | When an LLM generates plausible-sounding but factually incorrect information. RAG reduces this. |
| **System prompt** | An instruction message sent to the LLM before the user's question, controlling its behaviour. |
| **Chunk** | A smaller segment of a long document, embedded and stored as a separate Qdrant point. |
| **Overlap** | Characters shared between consecutive chunks so that context is not lost at boundaries. |

---

## Appendix B — Component overview

| Component | Role |
|-----------|------|
| **Qdrant** (Docker) | Open-source vector database — stores embeddings + payload metadata |
| **.NET 10 Web API** | Minimal API that exposes indexing, search, and chat endpoints |
| **OpenAI Embeddings** | `text-embedding-3-small` model (1536 dimensions) converts text → vectors |
| **OpenAI Chat** | `gpt-4o-mini` model generates answers grounded in retrieved documents |
| **Docker Compose** | Runs Qdrant in a container |

---

## Appendix C — Full API reference

All endpoints available in the completed solution:

| Endpoint | Method | Description | Introduced in |
|----------|--------|-------------|---------------|
| `/` | GET | Service info | Module 0 |
| `/health` | GET | Health check | Module 0 |
| `/documents` | POST | Index a single document | Module 1 |
| `/search/topk` | POST | Top-K similarity search | Module 2 |
| `/search/threshold` | POST | Threshold similarity search | Module 4 |
| `/search/metadata` | POST | Metadata-only search (no vector) | Module 4 |
| `/chat` | POST | Full RAG pipeline (retrieve + generate) | Module 5 |
| `/documents/batch` | POST | Batch document indexing | Module 8 |

See each module's README for detailed request/response examples.

---

## Appendix D — Code tour

| File | Responsibility |
|------|---------------|
| `Program.cs` | Config, DI, endpoint registration, Swagger |
| `Endpoints/ChatEndpoints.cs` | `POST /chat` — retrieve + generate (full RAG) |
| `Endpoints/DocumentEndpoints.cs` | `POST /documents` + `POST /documents/batch` |
| `Endpoints/SearchEndpoints.cs` | `POST /search/topk` + `/search/threshold` + `/search/metadata` |
| `Extensions/StringExtensions.cs` | `ToDeterministicGuid()` — deterministic point-id generation |
| `Extensions/DateTimeExtensions.cs` | `ToUnixMs()` — Unix timestamp helper |
| `Extensions/QdrantPayloadExtensions.cs` | gRPC payload → CLR dictionary conversion |
| `Models/PayloadKeys.cs` | Shared constants for Qdrant payload field names |
| `Models/Requests.cs` | All DTOs — upsert, search, chat requests/responses |
| `Models/ChunkingOptions.cs` | Chunking configuration (max size, overlap) |
| `Models/TextChunk.cs` | A single chunk produced by the text chunker |
| `Services/EmbeddingService.cs` | Text → vector embedding via OpenAI (`IEmbeddingGenerator<string, Embedding<float>>`) |
| `Services/DocumentIndexer.cs` | Embed + chunk + upsert orchestration |
| `Services/QdrantFilterFactory.cs` | Tag dictionary → Qdrant filter (gRPC + REST) |
| `Services/QdrantBootstrapper.cs` | Creates the Qdrant collection at startup with retries |
| `Services/TextChunker.cs` | Character-based text chunker with sentence-boundary awareness |

> **⚠️ Qdrant.Client SDK gotchas:**
>
> 1. **`Range` type ambiguity**: Always fully qualify as `Qdrant.Client.Grpc.Range`.
> 2. **`SearchAsync` parameters**: Use `payloadSelector: true` (not `withPayload`). The `limit` parameter is `ulong`. Use `scoreThreshold: float?` for cutoff. The `filter` parameter expects `Filter?`, not `Condition?`.
> 3. **Payload `Value` type**: Use `Qdrant.Client.Grpc.Value` (not `Google.Protobuf.WellKnownTypes.Value`). Use `KindOneofCase.DoubleValue` and `IntegerValue` (there is no `NumberValue`).

---

## Appendix E — Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `docker compose up` hangs | Docker Desktop not running | Start Docker Desktop, wait for engine |
| `[bootstrap] attempt N failed` repeating | Qdrant not ready yet | Wait — it retries 30 times |
| Search returns `[]` | No documents indexed | Index documents first with `POST /documents` |
| Tag filters return no results | No payload index on that field | Create a `keyword` index via Qdrant Dashboard |

---

## Appendix F — Extending the workshop

| Extension | Description |
|-----------|-------------|
| **Streaming chat** | Add `POST /chat/stream` with Server-Sent Events via `GetStreamingResponseAsync()` |
| **Multi-turn conversation** | Extend `ChatRequest` with a `History` field for follow-up questions |
| **Different vector DB** | Replace `QdrantClient` with Pinecone, Weaviate, Milvus, or ChromaDB SDK |
| **Model switching** | Change `EMBEDDING_MODEL` / `CHAT_MODEL` env vars (and set `LLM_ENDPOINT` if needed) |
| **Token-based chunking** | Replace character-based chunker with `Microsoft.ML.Tokenizers` for precision |
| **Custom metadata schemas** | Adapt tags/properties for your domain (e-commerce, support tickets, etc.) |
