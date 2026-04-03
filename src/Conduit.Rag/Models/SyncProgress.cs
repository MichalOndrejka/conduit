namespace Conduit.Rag.Models;

/// <summary>Transient in-memory sync progress for a source. Not persisted to disk.</summary>
/// <param name="Phase">"fetching" | "embedding"</param>
/// <param name="Current">Documents embedded so far (embedding phase only).</param>
/// <param name="Total">Total documents to embed (embedding phase only).</param>
public record SyncProgress(string Phase, int Current, int Total);
