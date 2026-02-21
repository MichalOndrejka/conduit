namespace Qdrant.Demo.Api.Models;

// ─────────────────────────────────────────────
// Upsert
// ─────────────────────────────────────────────

/// <summary>
/// Request body for <c>POST /documents</c> and <c>POST /documents/batch</c>.
/// </summary>
/// <param name="Id">
///   Optional caller-supplied identifier.  When provided the API generates a
///   deterministic Qdrant point-id from it, making re-indexing idempotent.
///   When omitted the point-id is derived from the <paramref name="Text"/> hash.
/// </param>
/// <param name="Text">
///   The content that will be embedded (turned into a vector).
///   This is the "knowledge" that similarity search operates on.
/// </param>
/// <param name="Tags">
///   <b>Indexed metadata</b> — key/value pairs that receive a Qdrant
///   <c>keyword</c> payload index and <b>can be used in search filters</b>.
///   <para>
///   Use tags for attributes you want to <b>filter by</b> when searching,
///   such as <c>category</c>, <c>author</c>, <c>language</c>, <c>source</c>,
///   or <c>department</c>.
///   </para>
///   <example>
///   <c>"tags": { "category": "science", "author": "Jane", "language": "en" }</c>
///   </example>
/// </param>
/// <param name="Properties">
///   <b>Informational metadata</b> — key/value pairs that are stored in the
///   Qdrant payload and <b>returned with search results</b>, but are
///   <b>not indexed</b> and <b>cannot be used in filters</b>.
///   <para>
///   Use properties for any extra context a consumer might need when reading
///   results, such as <c>source_url</c>, <c>summary</c>, <c>page_number</c>,
///   <c>created_date</c>, or <c>notes</c>.
///   </para>
///   <example>
///   <c>"properties": { "source_url": "https://…", "page": "42", "note": "intro chapter" }</c>
///   </example>
/// </param>
public record DocumentUpsertRequest(
    string? Id,
    string Text,
    Dictionary<string, string>? Tags = null,
    Dictionary<string, string>? Properties = null
);

/// <summary>Response returned after a successful upsert.</summary>
/// <param name="PointId">
///   The Qdrant point id of the stored document.
///   When chunking splits the text into multiple vectors, this is the id
///   of the <b>first</b> chunk — see <see cref="ChunkPointIds"/> for all.
/// </param>
/// <param name="TotalChunks">
///   How many chunks the document was split into (1 when it fit in a single embedding).
/// </param>
/// <param name="ChunkPointIds">
///   All Qdrant point ids produced — one per chunk.  For single-chunk
///   documents this list contains exactly the <see cref="PointId"/>.
/// </param>
public record DocumentUpsertResponse(
    string PointId,
    int TotalChunks = 1,
    IReadOnlyList<string>? ChunkPointIds = null
);

/// <summary>Response for a batch upsert.</summary>
public record BatchUpsertResponse(
    int Total,
    int Succeeded,
    int Failed,
    IReadOnlyList<string> Errors
);

// ─────────────────────────────────────────────
// Search — Top-K (fixed result count)
// ─────────────────────────────────────────────

/// <summary>
/// Vector similarity search that returns exactly <paramref name="K"/> results
/// (or fewer if the collection has less data), ranked by cosine similarity.
/// Optionally filtered by <paramref name="Tags"/>.
/// </summary>
/// <param name="QueryText">Free-text query that will be embedded and compared to stored vectors.</param>
/// <param name="K">Maximum number of results to return (default 5).</param>
/// <param name="Tags">
///   Optional tag filter — only documents whose <b>indexed tags</b> match
///   <b>all</b> supplied key/value pairs are returned.
/// </param>
public record TopKSearchRequest(
    string QueryText,
    int K = 5,
    Dictionary<string, string>? Tags = null
);

// ─────────────────────────────────────────────
// Search — Threshold (all above score)
// ─────────────────────────────────────────────

/// <summary>
/// Vector similarity search that returns <b>all</b> documents whose cosine
/// similarity score is ≥ <paramref name="ScoreThreshold"/>.
/// Use this when you want every "good enough" match, not a fixed count.
/// </summary>
/// <param name="QueryText">Free-text query that will be embedded.</param>
/// <param name="ScoreThreshold">
///   Minimum cosine-similarity score (0.0 – 1.0).
///   A good starting value is <c>0.4</c> — tune empirically for your data.
/// </param>
/// <param name="Limit">Safety cap — maximum results to return (default 100).</param>
/// <param name="Tags">Optional tag filter (same semantics as <see cref="TopKSearchRequest"/>).</param>
public record ThresholdSearchRequest(
    string QueryText,
    float ScoreThreshold = 0.4f,
    int Limit = 100,
    Dictionary<string, string>? Tags = null
);

// ─────────────────────────────────────────────
// Search — Metadata only (no vector)
// ─────────────────────────────────────────────

/// <summary>
/// Scroll through documents matching <b>only</b> tag filters — no vector
/// similarity is involved.  Useful for browsing / exporting subsets.
/// </summary>
/// <param name="Limit">Maximum number of documents to return (default 25).</param>
/// <param name="Tags">
///   Tag filter — only documents whose indexed tags match all supplied
///   key/value pairs are returned.  If omitted, all documents are returned.
/// </param>
public record MetadataSearchRequest(
    int Limit = 25,
    Dictionary<string, string>? Tags = null
);

// ─────────────────────────────────────────────
// Chat — RAG generation (retrieve + generate)
// ─────────────────────────────────────────────

/// <summary>
/// Request body for <c>POST /chat</c>.  The API embeds the
/// <paramref name="Question"/>, retrieves the top-K most similar documents
/// from Qdrant, and feeds them as context to the OpenAI chat-completion model.
/// </summary>
/// <param name="Question">Natural-language question to answer.</param>
/// <param name="K">
///   How many documents to retrieve as context (default 5).
///   More context = better grounding, but higher token cost.
/// </param>
/// <param name="ScoreThreshold">
///   Optional minimum similarity score.  Documents below this threshold are
///   excluded from the context even if they are in the top-K.
/// </param>
/// <param name="Tags">
///   Optional tag filter — restrict retrieval to documents whose indexed tags
///   match all supplied key/value pairs.
/// </param>
/// <param name="SystemPrompt">
///   Optional custom system prompt.  When omitted, a sensible default is used
///   that instructs the model to answer only from the provided context.
/// </param>
public record ChatRequest(
    string Question,
    int K = 5,
    float? ScoreThreshold = null,
    Dictionary<string, string>? Tags = null,
    string? SystemPrompt = null
);

/// <summary>Response returned by <c>POST /chat</c>.</summary>
/// <param name="Answer">The generated answer grounded in the retrieved documents.</param>
/// <param name="Sources">The documents used as context, with their similarity scores.</param>
public record ChatResponse(
    string Answer,
    IReadOnlyList<ChatSource> Sources
);

/// <summary>A single source document that contributed to the chat answer.</summary>
/// <param name="Id">Qdrant point id.</param>
/// <param name="Score">Cosine similarity score (0.0 – 1.0).</param>
/// <param name="TextSnippet">The document text that was used as context.</param>
public record ChatSource(
    string Id,
    float Score,
    string TextSnippet
);

// ─────────────────────────────────────────────
// Search result
// ─────────────────────────────────────────────

/// <summary>A single search result returned by the search endpoints.</summary>
/// <param name="Id">Qdrant point id.</param>
/// <param name="Score">Cosine similarity score (0.0 – 1.0).</param>
/// <param name="Payload">Full Qdrant payload as a dictionary.</param>
public record SearchHit(
    string? Id,
    float Score,
    Dictionary<string, object?> Payload
);
