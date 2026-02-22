using System.Text.RegularExpressions;

namespace Conduit.Rag.Parsing.Languages;

/// <summary>
/// Regex-based parser for TypeScript/JavaScript files.
/// Detects functions, classes, interfaces, type aliases, enums, and arrow function exports.
/// </summary>
public sealed partial class TypeScriptParser : ICodeParser
{
    private static readonly string[] SupportedExtensions =
        [".ts", ".tsx", ".js", ".jsx"];

    // Pattern table is static: GeneratedRegex methods return cached singletons.
    private static readonly (Regex Pattern, CodeUnitKind Kind, bool IsPublic)[] s_patterns =
    [
        (ExportedClassRegex(),     CodeUnitKind.Class,     true),
        (ExportedInterfaceRegex(), CodeUnitKind.Interface, true),
        (ExportedTypeRegex(),      CodeUnitKind.Type,      true),
        (ExportedEnumRegex(),      CodeUnitKind.Enum,      true),
        (ExportedFunctionRegex(),  CodeUnitKind.Function,  true),
        (ExportedArrowRegex(),     CodeUnitKind.Function,  true),
        (InternalFunctionRegex(),  CodeUnitKind.Function,  false),
        (InternalClassRegex(),     CodeUnitKind.Class,     false),
    ];

    public bool CanParse(string extension)
        => SupportedExtensions.Contains(extension.ToLowerInvariant());

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
        var units    = new List<CodeUnit>();
        var lines    = content.Split('\n');
        var hitLines = new HashSet<int>(); // tracks lines already consumed by a block

        for (var i = 0; i < lines.Length; i++)
        {
            if (hitLines.Contains(i)) continue;

            var line = lines[i];

            foreach (var (pattern, kind, isPublic) in s_patterns)
            {
                var match = pattern.Match(line);
                if (!match.Success) continue;

                var name      = match.Groups["name"].Value;
                var docComment = ExtractJsDoc(lines, i);
                var blockText = ParserUtils.ExtractBraceBlock(lines, i, out var endLine);

                hitLines.Add(i);
                for (var k = i + 1; k <= endLine; k++) hitLines.Add(k);

                units.Add(new CodeUnit
                {
                    Kind          = kind,
                    Name          = name,
                    ContainerName = null,
                    Namespace     = null,
                    Signature     = name,
                    IsPublic      = isPublic,
                    DocComment    = docComment,
                    FullText      = blockText,
                    Language      = "TypeScript",
                    FilePath      = filePath,
                });

                break;
            }
        }

        return units;
    }

    /// <summary>
    /// Scans upward from <paramref name="declarationLine"/> for a JSDoc block
    /// (<c>/** ... */</c>) immediately above the declaration.
    /// </summary>
    private static string? ExtractJsDoc(string[] lines, int declarationLine)
    {
        // Walk up past any blank lines
        var end = declarationLine - 1;
        while (end >= 0 && string.IsNullOrWhiteSpace(lines[end]))
            end--;

        // Expect closing */ of a JSDoc block
        if (end < 0 || !lines[end].TrimStart().StartsWith("*/"))
            return null;

        // Walk up to find the opening /**
        var start = end - 1;
        while (start >= 0 && !lines[start].TrimStart().StartsWith("/*"))
            start--;

        if (start < 0)
            return null;

        // Collect content lines between /** and */, stripping leading * and whitespace
        var docLines = lines[(start + 1)..end]
            .Select(l => l.TrimStart().TrimStart('*').Trim())
            .Where(l => l.Length > 0)
            .ToList();

        return docLines.Count > 0 ? string.Join("\n", docLines) : null;
    }

    [GeneratedRegex(@"^export\s+(default\s+)?class\s+(?<name>\w+)", RegexOptions.Compiled)]
    private static partial Regex ExportedClassRegex();

    [GeneratedRegex(@"^export\s+interface\s+(?<name>\w+)", RegexOptions.Compiled)]
    private static partial Regex ExportedInterfaceRegex();

    [GeneratedRegex(@"^export\s+type\s+(?<name>\w+)\s*=", RegexOptions.Compiled)]
    private static partial Regex ExportedTypeRegex();

    [GeneratedRegex(@"^export\s+enum\s+(?<name>\w+)", RegexOptions.Compiled)]
    private static partial Regex ExportedEnumRegex();

    [GeneratedRegex(@"^export\s+(default\s+)?function\s+(?<name>\w+)", RegexOptions.Compiled)]
    private static partial Regex ExportedFunctionRegex();

    [GeneratedRegex(@"^export\s+const\s+(?<name>\w+)\s*=\s*(\(|async\s*\(|async\s+\w)", RegexOptions.Compiled)]
    private static partial Regex ExportedArrowRegex();

    [GeneratedRegex(@"^function\s+(?<name>\w+)", RegexOptions.Compiled)]
    private static partial Regex InternalFunctionRegex();

    [GeneratedRegex(@"^class\s+(?<name>\w+)", RegexOptions.Compiled)]
    private static partial Regex InternalClassRegex();
}
