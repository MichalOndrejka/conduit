# Module 7 — Chunking

> **~25 min** · Builds on [Module 6](../module-06/README.md)

## Learning objective

By the end of this module you will have:

- Understood **why** long documents need to be split before embedding
- Implemented a character-based **text chunker** with sentence-boundary awareness
- Stored multi-chunk documents with parent-child metadata in Qdrant
- Seen how chunked documents appear in search results

---

## Concepts introduced

### Why chunk?

Embedding models have a **token limit** (e.g. `text-embedding-3-small` has a context window of 8,191 tokens). If your document is longer than that, the API call fails. Even shorter documents can produce lower-quality embeddings when the text is too long — the model has to compress too much meaning into a single vector.

**Chunking** splits a long document into smaller pieces, each of which gets its own embedding vector. This means:
- Each chunk is within the model's token limit
- Each vector represents a focused piece of meaning
- Search can return the most relevant *section*, not just the whole document

### Character-based approach

Our `TextChunker` uses **character counts** instead of token counts. Since typical tokenizers average ~4 characters per English token, 2000 characters ≈ 500 tokens — safely under the 8,191 limit.

### Sentence-boundary awareness

Naive chunking cuts at a fixed character count, which can break mid-sentence:

> `"Photosynthesis is the process by which green pla"` ← bad cut

The `TextChunker` scans backwards from the cut point to find the nearest sentence boundary (`.`, `?`, `!`, or `\n`), producing cleaner chunks.

### Overlap

Adjacent chunks share some text (default: 200 characters) so that context at boundaries isn't lost. If a sentence straddles two chunks, the overlap ensures it appears in at least one of them completely.

### Chunking metadata

When a document is split into multiple chunks, each Qdrant point gets extra payload fields:

| Field | Value |
|-------|-------|
| `source_doc_id` | The original document's point ID |
| `chunk_index` | Zero-based position (0, 1, 2, …) |
| `total_chunks` | How many chunks this document produced |

This lets you group search results by source document or fetch all chunks for a given document.

---

## What changed from Module 6

| New file | Purpose |
|----------|---------|
| `Models/ChunkingOptions.cs` | `MaxChunkSize` (default 2000) and `Overlap` (default 200) configuration |
| `Models/TextChunk.cs` | `TextChunk` record: Text, Index, StartOffset, EndOffset |
| `Services/ITextChunker.cs` | Interface for text chunking |
| `Services/TextChunker.cs` | Character-based chunker with sentence-boundary awareness |
| `Tests/TextChunkerTests.cs` | 18 tests covering short text, multi-chunk, overlap, boundary detection, edge cases |

| Changed file | What changed |
|-------------|-------------|
| `Models/PayloadKeys.cs` | Added `SourceDocId`, `ChunkIndex`, `TotalChunks` constants |
| `Models/Requests.cs` | `DocumentUpsertResponse` now includes `TotalChunks` and `ChunkPointIds` |
| `Services/DocumentIndexer.cs` | Full rewrite: chunks text, creates a point per chunk, stores chunking metadata |
| `Program.cs` | Registers `ChunkingOptions`, `ITextChunker`; passes chunker to `DocumentIndexer` |

### Code walkthrough

#### The text chunker — [`TextChunker.cs`](src/Qdrant.Demo.Api/Services/TextChunker.cs)

If the text fits in a single chunk, it's returned as-is. Otherwise, the chunker splits with overlap and tries to break at sentence boundaries:

```csharp
public IReadOnlyList<TextChunk> Chunk(string text)
{
    if (text.Length <= options.MaxChunkSize)
    {
        return [new TextChunk(text, Index: 0, StartOffset: 0, EndOffset: text.Length)];
    }

    List<TextChunk> chunks = [];
    var chunkIndex = 0;
    var start = 0;

    while (start < text.Length)
    {
        var remaining = text.Length - start;
        var length = Math.Min(options.MaxChunkSize, remaining);

        // Not the last chunk? Try to find a sentence boundary to break at.
        if (start + length < text.Length)
        {
            length = FindSentenceBoundary(text, start, length);
        }
        // ... store chunk and advance by (length - overlap)
    }
}
```

The `FindSentenceBoundary` method scans backwards from the cut point, preferring paragraph breaks (`\n`), then sentence enders (`.`, `?`, `!`), then any whitespace — and falls back to a hard cut only as a last resort.

#### Chunked indexing — [`DocumentIndexer.cs`](src/Qdrant.Demo.Api/Services/DocumentIndexer.cs)

The indexer now creates one Qdrant point per chunk. Single-chunk documents keep the original id; multi-chunk documents get a derived id per chunk:

```csharp
// For single-chunk documents keep the original id;
// for multi-chunk, append _chunk_{index} for uniqueness.
var pointIdStr = chunks.Count == 1
    ? sourceId
    : $"{sourceId}_chunk_{i}".ToDeterministicGuid().ToString("D");
```

Each chunk carries parent-child metadata so search results can be grouped by source document:

```csharp
if (chunks.Count > 1)
{
    point.Payload[SourceDocId] = sourceId;
    point.Payload[ChunkIndex] = i.ToString();
    point.Payload[TotalChunks] = chunks.Count.ToString();
}
```

Tags and properties from the original request are copied to **every** chunk, so tag-filtered searches still match regardless of which chunk is most relevant.

---

## Step 1 — Start Qdrant and run the API

```bash
cd module-07
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

Visit `http://localhost:8080/` — the response now includes a `chunking` section showing the configured `maxChunkSize` and `overlap`.

## Step 2 — Index a short document (single chunk)

1. Open **Swagger UI** in your browser: **http://localhost:8080/swagger**
2. Find the **POST /documents** endpoint, click **Try it out**
3. Paste the following JSON body and click **Execute**:

```json
{
  "id": "short-001",
  "text": "Photosynthesis converts sunlight into chemical energy."
}
```

In the **Response body** you should see:

```json
{
  "pointId": "abc-123...",
  "totalChunks": 1,
  "chunkPointIds": ["abc-123..."]
}
```

`totalChunks: 1` — the text was short enough for a single embedding.

## Step 3 — Index a long document (multiple chunks)

Using **POST /documents** in Swagger UI, paste the following JSON body and click **Execute**. The text is over 3,000 characters, so the chunker will split it into multiple chunks automatically:

```json
{
  "id": "history-coffee-001",
  "text": "The history of coffee begins in the ancient highlands of Ethiopia, where legend credits a goat herder named Kaldi with its discovery around the ninth century. Kaldi noticed that his goats became unusually energetic after eating berries from a certain tree. Curious, he tried the berries himself and experienced a similar burst of alertness. He brought the berries to a local monastery, where monks brewed them into a drink that helped them stay awake during long evening prayers. Word of the energizing beverage spread quickly, and by the fifteenth century coffee was being cultivated in the terraced hills of Yemen. Sufi monks in particular valued the drink for its ability to sustain deep concentration during nighttime devotions and marathon sessions of prayer. The port city of Mocha on the Red Sea coast became a major hub for the coffee trade, giving its name to a style of coffee still recognized today. From Yemen the drink soon reached Persia, Egypt, Syria, and the Ottoman Empire.\n\nCoffeehouses, known as qahveh khaneh, began appearing in cities across the Middle East during the sixteenth century. These establishments quickly became vibrant centers of social and intellectual activity, earning the nickname 'Schools of the Wise.' Patrons gathered to drink coffee, listen to music, watch performers, play chess and backgammon, and debate the political and philosophical questions of the day. The stimulating atmosphere of coffeehouses occasionally attracted suspicion from political and religious authorities. In Mecca, Cairo, and Constantinople, rulers attempted outright bans on coffee, arguing that it encouraged free thinking and seditious conversation. None of these prohibitions lasted long, however, because the drink's popularity proved impossible to suppress.\n\nEuropean travelers to the Near East brought back stories of the unusual dark beverage, and by the early seventeenth century coffee had arrived on the continent. It was initially met with deep suspicion. Some Catholic clergy in Italy denounced it as the 'bitter invention of Satan,' urging Pope Clement VIII to ban it outright. According to popular legend, the Pope insisted on tasting the drink before passing judgment, found it delicious, and gave it his papal blessing instead. Coffeehouses then sprang up rapidly across Europe. In England they became known as 'penny universities,' because for the price of a single penny one could purchase a cup of coffee and engage in hours of stimulating conversation with scholars, merchants, and politicians. By the mid-seventeenth century London alone boasted over three hundred coffeehouses, each attracting its own clientele of traders, writers, scientists, and artists. Lloyd's of London, the famous insurance market, began life as Edward Lloyd's coffeehouse, where ship owners and underwriters gathered to do business.\n\nThe Dutch were among the first Europeans to obtain live coffee plants and began cultivating them in their colonial territories in Java and Suriname during the late seventeenth century. France soon followed, establishing sprawling coffee plantations across the Caribbean islands of Martinique and Saint-Domingue. A single coffee plant gifted to King Louis XIV became the ancestor of millions of trees across Central and South America. The Portuguese brought coffee to Brazil in the eighteenth century, and within a hundred years Brazil had become the world's dominant producer, a position it still holds today. Meanwhile, the Boston Tea Party of 1773, in which American colonists protested British taxation by dumping tea into Boston Harbor, helped shift the young nation's preference decisively from tea to coffee. Coffee became a patriotic drink, and its association with American identity has endured ever since. Today coffee is the second most traded commodity on Earth after crude oil, sustaining the livelihoods of over twenty-five million farming families and fueling a global industry worth more than four hundred billion dollars a year, with over two billion cups consumed every single day.",
  "tags": { "category": "history" }
}
```

In the **Response body** you should see `totalChunks: 4` and a `chunkPointIds` array with one entry per chunk:

```json
{
  "pointId": "b61e52cb-d639-1056-874c-0b77556478f5",
  "totalChunks": 4,
  "chunkPointIds": [
    "b61e52cb-d639-1056-874c-0b77556478f5",
    "b9314d56-b613-7554-9408-f110a5af0d0d",
    "d4540e80-f35a-8c50-83e7-1d80b7282342",
    "5ab70094-a820-d854-b12f-08bdf57cdf19"
  ]
}
```

## Step 4 — Search and observe chunk results

In **Swagger UI**, find the **POST /search/topk** endpoint, click **Try it out**, paste the following body and click **Execute**:

```json
{
  "queryText": "How did coffee spread from Africa to Europe?",
  "k": 5
}
```

You may see multiple chunks from the same source document in the results, each with its own score. The payload will include `source_doc_id`, `chunk_index`, and `total_chunks`. Notice how different chunks match with different scores — the chunk that mentions European arrival should rank highest.

## Step 5 — Chat with chunked documents

The `/chat` endpoint works seamlessly with chunks — it retrieves the most relevant chunks (not whole documents) and feeds them as context to the LLM.

In **Swagger UI**, find the **POST /chat** endpoint, click **Try it out**, paste the following body and click **Execute**:

```json
{
  "question": "How did coffee spread from Africa to Europe and what role did coffeehouses play?",
  "k": 5
}
```

The LLM now gets the most relevant **chunks** as context, not entire documents. Because the coffee article was split into 4 chunks, the model pulls from exactly the chunks that cover the European expansion and coffeehouse culture — giving a more focused and accurate answer than if it received one giant text block.

Try a question that spans multiple chunks:

```json
{
  "question": "Compare the role of coffeehouses in the Middle East versus England"
}
```

The RAG pipeline retrieves chunks from different parts of the same document, assembling cross-section context automatically.

---

## Exercises

### Exercise 7.1 — Custom chunk size

Set a small chunk size via environment variable and re-run:

```bash
CHUNKING_MAX_SIZE=200 CHUNKING_OVERLAP=50 dotnet run
```

```powershell
# PowerShell alternative
$env:CHUNKING_MAX_SIZE = "200"
$env:CHUNKING_OVERLAP = "50"
dotnet run
```

Index the same long document and observe that it produces many more chunks.

### Exercise 7.2 — Inspect sentence boundaries

Index this text and check where the chunks split:

```
First paragraph is about biology. Photosynthesis is important. Plants need sunlight.
Second paragraph discusses physics. Quantum mechanics is complex. Particles behave strangely.
Third paragraph covers chemistry. Chemical reactions produce energy. Molecules combine.
```

The chunker should prefer splitting at paragraph breaks or sentence ends.

---

## ✅ Checkpoint

At this point you have:

- [x] Automatic text chunking with configurable size and overlap
- [x] Sentence-boundary-aware splitting (no mid-sentence cuts)
- [x] Multi-chunk documents with parent-child metadata in Qdrant
- [x] Understanding of: chunking strategy, overlap, sentence boundaries, chunking metadata

## 🧹 Clean Up

Before moving to the next module, stop everything started in this module:

1. **Stop the local API** — press `Ctrl+C` in the terminal where `dotnet run` is running
2. **Reset environment variables** — if you changed chunk settings in Exercise 7.1, clear them so the next module uses defaults:

```powershell
# PowerShell
Remove-Item Env:CHUNKING_MAX_SIZE -ErrorAction SilentlyContinue
Remove-Item Env:CHUNKING_OVERLAP -ErrorAction SilentlyContinue
```

```bash
# Linux/macOS
unset CHUNKING_MAX_SIZE CHUNKING_OVERLAP
```

3. **Stop Docker containers** — from the `module-07` directory:

```bash
docker compose down
```

This stops Qdrant so the next module starts fresh.

**Next →** [Module 8 — Batch Operations](../module-08/README.md)
