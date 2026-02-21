using System.Text.Json;
using Conduit.Rag.Models;

namespace Conduit.Rag.Services;

public sealed class JsonSourceConfigStore(string filePath) : ISourceConfigStore
{
    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        WriteIndented = true,
        PropertyNameCaseInsensitive = true
    };

    private readonly SemaphoreSlim _lock = new(1, 1);

    public async Task<IReadOnlyList<SourceDefinition>> GetAllAsync(CancellationToken ct = default)
    {
        await _lock.WaitAsync(ct);
        try
        {
            return await ReadAllAsync();
        }
        finally
        {
            _lock.Release();
        }
    }

    public async Task<SourceDefinition?> GetByIdAsync(string id, CancellationToken ct = default)
    {
        var all = await GetAllAsync(ct);
        return all.FirstOrDefault(s => s.Id == id);
    }

    public async Task SaveAsync(SourceDefinition source, CancellationToken ct = default)
    {
        await _lock.WaitAsync(ct);
        try
        {
            var all = (await ReadAllAsync()).ToList();
            var idx = all.FindIndex(s => s.Id == source.Id);

            if (idx >= 0)
                all[idx] = source;
            else
                all.Add(source);

            await WriteAllAsync(all);
        }
        finally
        {
            _lock.Release();
        }
    }

    public async Task DeleteAsync(string id, CancellationToken ct = default)
    {
        await _lock.WaitAsync(ct);
        try
        {
            var all = (await ReadAllAsync()).Where(s => s.Id != id).ToList();
            await WriteAllAsync(all);
        }
        finally
        {
            _lock.Release();
        }
    }

    private async Task<List<SourceDefinition>> ReadAllAsync()
    {
        if (!File.Exists(filePath)) return [];

        await using var stream = File.OpenRead(filePath);
        return await JsonSerializer.DeserializeAsync<List<SourceDefinition>>(stream, JsonOptions) ?? [];
    }

    private async Task WriteAllAsync(List<SourceDefinition> sources)
    {
        var dir = Path.GetDirectoryName(filePath);
        if (!string.IsNullOrEmpty(dir))
            Directory.CreateDirectory(dir);

        await using var stream = File.Create(filePath);
        await JsonSerializer.SerializeAsync(stream, sources, JsonOptions);
    }
}
