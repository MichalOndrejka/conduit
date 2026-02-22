using System.Text.RegularExpressions;

namespace Conduit.Rag.Parsing.Languages;

/// <summary>
/// Regex-based parser for Go source files.
/// Detects func declarations, struct types, and interface types.
/// Exported = uppercase first letter.
/// </summary>
public sealed partial class GoParser : ICodeParser
{
    public bool CanParse(string extension)
        => extension.Equals(".go", StringComparison.OrdinalIgnoreCase);

    public IReadOnlyList<CodeUnit> Parse(string content, string filePath)
    {
        try
        {
            return ParseInternal(content, filePath);
        }
        catch
        {
            return [];
        }
    }

    private static IReadOnlyList<CodeUnit> ParseInternal(string content, string filePath)
    {
        var units   = new List<CodeUnit>();
        var lines   = content.Split('\n');
        var package = ExtractPackage(lines);

        for (var i = 0; i < lines.Length; i++)
        {
            var line = lines[i];

            // func (receiver) Name(...) ...
            var funcMatch = FuncRegex().Match(line);
            if (funcMatch.Success)
            {
                var name            = funcMatch.Groups["name"].Value;
                var receiver        = funcMatch.Groups["receiver"].Value.Trim();
                var declarationLine = i;                                    // capture before i is moved
                var block           = ParserUtils.ExtractBraceBlock(lines, i, out var endLine);
                i = endLine;

                units.Add(new CodeUnit
                {
                    Kind          = CodeUnitKind.Function,
                    Name          = name,
                    ContainerName = string.IsNullOrEmpty(receiver) ? null : StripPointer(receiver),
                    Namespace     = package,
                    Signature     = line.Trim(),
                    IsPublic      = name.Length > 0 && char.IsUpper(name[0]),
                    DocComment    = ExtractGoDoc(lines, declarationLine),   // use declaration line
                    FullText      = block,
                    Language      = "Go",
                    FilePath      = filePath,
                });
                continue;
            }

            // type Name struct { ... }
            var structMatch = StructRegex().Match(line);
            if (structMatch.Success)
            {
                var name            = structMatch.Groups["name"].Value;
                var declarationLine = i;
                var block           = ParserUtils.ExtractBraceBlock(lines, i, out var endLine);
                i = endLine;

                units.Add(new CodeUnit
                {
                    Kind          = CodeUnitKind.Struct,
                    Name          = name,
                    ContainerName = null,
                    Namespace     = package,
                    Signature     = null,
                    IsPublic      = name.Length > 0 && char.IsUpper(name[0]),
                    DocComment    = ExtractGoDoc(lines, declarationLine),
                    FullText      = block,
                    Language      = "Go",
                    FilePath      = filePath,
                });
                continue;
            }

            // type Name interface { ... }
            var ifaceMatch = InterfaceRegex().Match(line);
            if (ifaceMatch.Success)
            {
                var name            = ifaceMatch.Groups["name"].Value;
                var declarationLine = i;
                var block           = ParserUtils.ExtractBraceBlock(lines, i, out var endLine);
                i = endLine;

                units.Add(new CodeUnit
                {
                    Kind          = CodeUnitKind.Interface,
                    Name          = name,
                    ContainerName = null,
                    Namespace     = package,
                    Signature     = null,
                    IsPublic      = name.Length > 0 && char.IsUpper(name[0]),
                    DocComment    = ExtractGoDoc(lines, declarationLine),
                    FullText      = block,
                    Language      = "Go",
                    FilePath      = filePath,
                });
            }
        }

        return units;
    }

    /// <summary>Scans upward from <paramref name="declarationLine"/> collecting // comment lines.</summary>
    private static string? ExtractGoDoc(string[] lines, int declarationLine)
    {
        var docLines = new List<string>();
        var i        = declarationLine - 1;

        while (i >= 0 && lines[i].TrimStart().StartsWith("//"))
        {
            docLines.Insert(0, lines[i].TrimStart().TrimStart('/').Trim());
            i--;
        }

        return docLines.Count > 0 ? string.Join("\n", docLines) : null;
    }

    private static string? ExtractPackage(string[] lines)
    {
        foreach (var line in lines)
        {
            var m = PackageRegex().Match(line);
            if (m.Success) return m.Groups["pkg"].Value;
        }
        return null;
    }

    private static string StripPointer(string receiver)
    {
        // e.g. "r *MyType" → "MyType"
        return receiver.Split(' ').Last().TrimStart('*');
    }

    [GeneratedRegex(@"^func\s+(\((?<receiver>[^)]*)\)\s+)?(?<name>[A-Za-z_]\w*)\s*\(", RegexOptions.Compiled)]
    private static partial Regex FuncRegex();

    [GeneratedRegex(@"^type\s+(?<name>[A-Za-z_]\w*)\s+struct\b", RegexOptions.Compiled)]
    private static partial Regex StructRegex();

    [GeneratedRegex(@"^type\s+(?<name>[A-Za-z_]\w*)\s+interface\b", RegexOptions.Compiled)]
    private static partial Regex InterfaceRegex();

    [GeneratedRegex(@"^package\s+(?<pkg>\w+)", RegexOptions.Compiled)]
    private static partial Regex PackageRegex();
}
