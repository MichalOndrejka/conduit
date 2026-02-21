using Conduit.Rag.Ado;
using Conduit.Rag.Models;

namespace Conduit.Rag.Sources;

public sealed class SourceFactory(IAdoClient adoClient)
{
    public ISource Create(SourceDefinition definition) => definition.Type switch
    {
        SourceTypes.ManualDocument   => new ManualDocumentSource(definition),
        SourceTypes.AdoWorkItemQuery => new AdoWorkItemQuerySource(definition, adoClient),
        SourceTypes.AdoCodeRepo      => new AdoCodeRepoSource(definition, adoClient),
        SourceTypes.AdoPipelineBuild => new AdoPipelineBuildSource(definition, adoClient),
        SourceTypes.AdoRequirements  => new AdoRequirementsSource(definition, adoClient),
        SourceTypes.AdoTestCase      => new AdoTestCaseSource(definition, adoClient),
        _ => throw new ArgumentException($"Unknown source type: '{definition.Type}'", nameof(definition))
    };
}
