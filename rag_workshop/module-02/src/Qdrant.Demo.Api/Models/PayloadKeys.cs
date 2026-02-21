namespace Qdrant.Demo.Api.Models;

/// <summary>
/// Well-known Qdrant payload field names used across the application.
/// Centralised here so that indexing and retrieval always agree on the keys.
/// </summary>
public static class PayloadKeys
{
    /// <summary>The payload field that stores the original document text.</summary>
    public const string Text = "text";

    /// <summary>The payload field that stores the indexing timestamp (Unix ms).</summary>
    public const string IndexedAtMs = "indexed_at_ms";
}
