namespace Conduit.Rag.Models;

public record SourceDocument(
    string Id,
    string Text,
    Dictionary<string, string> Tags,
    Dictionary<string, string> Properties
);
