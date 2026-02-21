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

    /// <summary>Prefix for filterable tag fields (<c>tag_{key}</c>).</summary>
    public const string TagPrefix = "tag_";

    /// <summary>Prefix for informational property fields (<c>prop_{key}</c>).</summary>
    public const string PropertyPrefix = "prop_";
}
