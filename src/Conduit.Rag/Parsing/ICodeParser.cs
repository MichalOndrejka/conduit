namespace Conduit.Rag.Parsing;

/// <summary>
/// Parses source code or markup into logical <see cref="CodeUnit"/> instances.
/// Implementations must never throw — return an empty list on parse errors.
/// </summary>
public interface ICodeParser
{
    /// <summary>Returns true if this parser handles the given file extension (e.g. ".cs").</summary>
    bool CanParse(string extension);

    /// <summary>Parses <paramref name="content"/> and returns one unit per logical block.</summary>
    IReadOnlyList<CodeUnit> Parse(string content, string filePath);
}
