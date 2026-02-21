using Conduit.Rag.Models;

namespace Conduit.Rag.Sources;

/// <summary>A single manually entered document (architecture doc, ADR, design note, etc.).</summary>
public sealed class ManualDocumentSource(SourceDefinition definition) : ISource
{
    public string Type           => SourceTypes.ManualDocument;
    public string CollectionName => CollectionNames.ManualDocuments;

    public Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(CancellationToken ct = default)
    {
        definition.Config.TryGetValue(ConfigKeys.Title, out var title);
        var content = definition.Config.GetValueOrDefault(ConfigKeys.Content, string.Empty);

        var document = new SourceDocument(
            Id:         definition.Id,
            Text:       content,
            Tags:       new Dictionary<string, string>
            {
                ["source_name"] = definition.Name
            },
            Properties: new Dictionary<string, string>
            {
                ["title"] = title ?? definition.Name
            }
        );

        return Task.FromResult<IReadOnlyList<SourceDocument>>([document]);
    }
}
