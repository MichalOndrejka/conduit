namespace Qdrant.Demo.Api.Models;

// ─────────────────────────────────────────────
// Upsert
// ─────────────────────────────────────────────

/// <summary>
/// Request body for <c>POST /documents</c>.
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
///   Indexed metadata (used for filtering in later modules). Optional for now.
/// </param>
/// <param name="Properties">
///   Informational metadata (returned with results, not filterable). Optional for now.
/// </param>
public record DocumentUpsertRequest(
    string? Id,
    string Text,
    Dictionary<string, string>? Tags = null,
    Dictionary<string, string>? Properties = null
);

/// <summary>Response returned after a successful upsert.</summary>
/// <param name="PointId">The Qdrant point id of the stored document.</param>
public record DocumentUpsertResponse(string PointId);

// ─────────────────────────────────────────────
// Search — Top-K (fixed result count)
// ─────────────────────────────────────────────

/// <summary>
/// Vector similarity search that returns exactly <paramref name="K"/> results
/// (or fewer if the collection has less data), ranked by cosine similarity.
/// </summary>
/// <param name="QueryText">Free-text query that will be embedded and compared to stored vectors.</param>
/// <param name="K">Maximum number of results to return (default 5).</param>
/// <param name="Tags">Optional tag filter (used in later modules). Ignored for now.</param>
public record TopKSearchRequest(
    string QueryText,
    int K = 5,
    Dictionary<string, string>? Tags = null
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
