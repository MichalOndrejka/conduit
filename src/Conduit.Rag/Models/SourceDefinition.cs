namespace Conduit.Rag.Models;

public class SourceDefinition
{
    public string Id { get; set; } = Guid.NewGuid().ToString("D");
    public string Type { get; set; } = string.Empty;
    public string Name { get; set; } = string.Empty;
    public bool Enabled { get; set; } = true;
    public DateTime? LastSyncedAt { get; set; }
    /// <summary>idle | syncing | completed | failed</summary>
    public string SyncStatus { get; set; } = "idle";
    public string? SyncError { get; set; }
    public Dictionary<string, string> Config { get; set; } = new();
}
