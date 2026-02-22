using System.Text.RegularExpressions;

namespace Conduit.Rag.Parsing.Languages;

/// <summary>
/// Regex-based parser for PowerShell files (.ps1, .psm1).
/// Detects function declarations and extracts comment-based help (.SYNOPSIS).
/// </summary>
public sealed partial class PowerShellParser : ICodeParser
{
    private static readonly string[] SupportedExtensions = [".ps1", ".psm1"];

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
        var units = new List<CodeUnit>();
        var lines = content.Split('\n');

        for (var i = 0; i < lines.Length; i++)
        {
            var match = FunctionRegex().Match(lines[i]);
            if (!match.Success) continue;

            var name            = match.Groups["name"].Value;
            var declarationLine = i;
            var docComment      = ExtractCommentHelp(lines, declarationLine);
            var block           = ParserUtils.ExtractBraceBlock(lines, i, out var endLine);
            i = endLine;

            units.Add(new CodeUnit
            {
                Kind          = CodeUnitKind.Function,
                Name          = name,
                ContainerName = null,
                Namespace     = null,
                Signature     = name,
                IsPublic      = !name.StartsWith('_'),
                DocComment    = docComment,
                FullText      = block,
                Language      = "PowerShell",
                FilePath      = filePath,
            });
        }

        return units;
    }

    /// <summary>
    /// Looks upward from <paramref name="declarationLine"/> for a
    /// <c>&lt;# .SYNOPSIS ... #&gt;</c> comment-based help block.
    /// </summary>
    private static string? ExtractCommentHelp(string[] lines, int declarationLine)
    {
        // Find the closing #> on the line(s) immediately before the function
        var endHelp = -1;
        for (var i = declarationLine - 1; i >= 0; i--)
        {
            var trimmed = lines[i].TrimStart();
            if (trimmed.StartsWith("#>"))  { endHelp = i; break; }
            if (!string.IsNullOrWhiteSpace(trimmed)) break; // stop on non-blank, non-#> line
        }

        if (endHelp < 0) return null;

        // Find the opening <#
        var startHelp = -1;
        for (var i = endHelp - 1; i >= 0; i--)
        {
            if (lines[i].TrimStart().StartsWith("<#")) { startHelp = i; break; }
        }

        if (startHelp < 0) return null;

        // Extract .SYNOPSIS section
        var synopsis    = new List<string>();
        var inSynopsis  = false;

        foreach (var raw in lines[startHelp..(endHelp + 1)])
        {
            var line = raw.Trim().TrimStart('<').TrimStart('#').TrimEnd('>').TrimEnd('#').Trim();

            if (line.StartsWith(".SYNOPSIS", StringComparison.OrdinalIgnoreCase))
            {
                inSynopsis = true;
                continue;
            }

            if (line.StartsWith('.') && inSynopsis) break;

            if (inSynopsis && line.Length > 0)
                synopsis.Add(line);
        }

        return synopsis.Count > 0 ? string.Join(" ", synopsis) : null;
    }

    [GeneratedRegex(@"^(?:function|Function)\s+(?<name>[\w\-:]+)", RegexOptions.Compiled)]
    private static partial Regex FunctionRegex();
}
