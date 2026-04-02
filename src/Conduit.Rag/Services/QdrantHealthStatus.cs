namespace Conduit.Rag.Services;

public sealed class QdrantHealthStatus
{
    public bool    IsReady { get; set; }
    public string? Error   { get; set; }
}
