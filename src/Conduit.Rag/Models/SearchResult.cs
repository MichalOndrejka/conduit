namespace Conduit.Rag.Models;

public record SearchResult(
    string Id,
    float Score,
    string Text,
    Dictionary<string, string> Tags,
    Dictionary<string, string> Properties
);
