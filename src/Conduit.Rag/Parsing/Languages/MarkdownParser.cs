using System.Text.RegularExpressions;

namespace Conduit.Rag.Parsing.Languages;

/// <summary>
/// Heading-based parser for Markdown files.
/// Each #/##/### section becomes one <see cref="CodeUnit"/>.
/// Files with no headings are returned as a single whole-file unit.
/// </summary>
public sealed partial class MarkdownParser : ICodeParser
{
    public bool CanParse(string extension)
        => extension.Equals(".md", StringComparison.OrdinalIgnoreCase)
        || extension.Equals(".mdx", StringComparison.OrdinalIgnoreCase);

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
        var lines   = content.Split('\n');
        var units   = new List<CodeUnit>();
        var heading = HeadingRegex();

        // Collect heading positions
        var headings = new List<(int Line, string Name)>();
        for (var i = 0; i < lines.Length; i++)
        {
            var m = heading.Match(lines[i]);
            if (m.Success) headings.Add((i, m.Groups["title"].Value.Trim()));
        }

        if (headings.Count == 0)
        {
            // Whole file as one unit
            units.Add(new CodeUnit
            {
                Kind          = CodeUnitKind.Section,
                Name          = System.IO.Path.GetFileNameWithoutExtension(filePath),
                ContainerName = null,
                Namespace     = null,
                Signature     = null,
                IsPublic      = true,
                DocComment    = null,
                FullText      = content.Trim(),
                Language      = "Markdown",
                FilePath      = filePath,
            });
            return units;
        }

        for (var h = 0; h < headings.Count; h++)
        {
            var (startLine, name) = headings[h];
            var endLine           = h + 1 < headings.Count ? headings[h + 1].Line - 1 : lines.Length - 1;

            var sectionLines = lines[startLine..(endLine + 1)];
            var sectionText  = string.Join("\n", sectionLines).Trim();

            units.Add(new CodeUnit
            {
                Kind          = CodeUnitKind.Section,
                Name          = name,
                ContainerName = null,
                Namespace     = null,
                Signature     = null,
                IsPublic      = true,
                DocComment    = null,
                FullText      = sectionText,
                Language      = "Markdown",
                FilePath      = filePath,
            });
        }

        return units;
    }

    [GeneratedRegex(@"^#{1,6}\s+(?<title>.+)$", RegexOptions.Compiled)]
    private static partial Regex HeadingRegex();
}
