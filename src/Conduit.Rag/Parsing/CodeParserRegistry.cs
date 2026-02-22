namespace Conduit.Rag.Parsing;

/// <summary>
/// Resolves the appropriate <see cref="ICodeParser"/> for a given file extension.
/// Receives all registered parsers via dependency injection.
/// </summary>
public sealed class CodeParserRegistry(IEnumerable<ICodeParser> parsers)
{
    private readonly IReadOnlyList<ICodeParser> _parsers = parsers.ToList();

    /// <summary>
    /// Returns the first parser that can handle <paramref name="extension"/>,
    /// or <c>null</c> if none match.
    /// </summary>
    public ICodeParser? Resolve(string extension)
        => _parsers.FirstOrDefault(p => p.CanParse(extension));
}
