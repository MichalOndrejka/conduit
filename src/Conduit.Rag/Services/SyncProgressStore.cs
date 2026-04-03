using System.Collections.Concurrent;
using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public sealed class SyncProgressStore
{
    private readonly ConcurrentDictionary<string, SyncProgress> _progress = new();

    public void Set(string sourceId, SyncProgress progress) => _progress[sourceId] = progress;

    public SyncProgress? Get(string sourceId) =>
        _progress.TryGetValue(sourceId, out var p) ? p : null;

    public void Clear(string sourceId) => _progress.TryRemove(sourceId, out _);
}
