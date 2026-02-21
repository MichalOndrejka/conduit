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
