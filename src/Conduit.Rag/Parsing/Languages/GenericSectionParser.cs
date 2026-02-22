namespace Conduit.Rag.Parsing.Languages;

/// <summary>
/// Returns the whole file as a single <see cref="CodeUnit"/> for formats
/// that don't have parseable structure: YAML, JSON, Dockerfile.
/// </summary>
public sealed class GenericSectionParser : ICodeParser
{
    private static readonly string[] SupportedExtensions =
        [".yaml", ".yml", ".json", "dockerfile"];

    public bool CanParse(string extension)
        => SupportedExtensions.Contains(extension.ToLowerInvariant());

    public IReadOnlyList<CodeUnit> Parse(string content, string filePath)
    {
        try
        {
            var name = System.IO.Path.GetFileName(filePath);
            var ext  = System.IO.Path.GetExtension(filePath).TrimStart('.').ToUpperInvariant();
            if (string.IsNullOrEmpty(ext)) ext = "File";

            return
            [
                new CodeUnit
                {
                    Kind          = CodeUnitKind.File,
                    Name          = name,
                    ContainerName = null,
                    Namespace     = null,
                    Signature     = null,
                    IsPublic      = true,
                    DocComment    = null,
                    FullText      = content.Trim(),
                    Language      = ext,
                    FilePath      = filePath,
                }
            ];
        }
        catch
        {
            return [];
        }
    }
}
