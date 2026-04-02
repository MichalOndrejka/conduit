using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Parsing;

namespace Conduit.Rag.Sources;

/// <summary>Fetches source files from an ADO Git repository matching configured glob patterns.</summary>
public sealed class AdoCodeRepoSource(SourceDefinition definition, IAdoClient ado, CodeParserRegistry parserRegistry) : ISource
{
    public string Type           => SourceTypes.AdoCodeRepo;
    public string CollectionName => CollectionNames.AdoCode;

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(CancellationToken ct = default)
    {
        var conn     = AdoConnectionConfig.From(definition.Config);
        var repo     = definition.Config[ConfigKeys.Repository];
        var branch   = definition.Config.GetValueOrDefault(ConfigKeys.Branch, "main");
        var patterns = ParsePatterns(definition.Config.GetValueOrDefault(ConfigKeys.GlobPatterns, "**/*.cs"));

        var allPaths = await ado.GetFileTreeAsync(conn, repo, branch, ct: ct);
        var matchingPaths = allPaths.Where(p => patterns.Any(pattern => GlobMatch(pattern, p))).ToList();

        var documents = new List<SourceDocument>(matchingPaths.Count);

        foreach (var path in matchingPaths)
        {
            ct.ThrowIfCancellationRequested();

            try
            {
                var content = await ado.GetFileContentAsync(conn, repo, branch, path, ct);
                if (string.IsNullOrWhiteSpace(content)) continue;

                var ext = Path.GetExtension(path).ToLowerInvariant();
                // For extension-less files (e.g. Dockerfile), fall back to the filename as the key.
                var parserKey = string.IsNullOrEmpty(ext)
                    ? Path.GetFileName(path).ToLowerInvariant()
                    : ext;
                var parser = parserRegistry.Resolve(parserKey);
                var units  = parser?.Parse(content, path) ?? [];

                if (units.Count > 0)
                {
                    // Emit one document per parsed code unit
                    foreach (var unit in units)
                    {
                        var idSlug = unit.ToIdSlug();
                        documents.Add(new SourceDocument(
                            Id:   $"code-{repo}-{branch}-{path}-{idSlug}",
                            Text: unit.EnrichedText,
                            Tags: new Dictionary<string, string>
                            {
                                ["source_name"] = definition.Name,
                                ["repository"]  = repo,
                                ["file_ext"]    = ext.TrimStart('.'),
                                ["code_kind"]   = unit.Kind.ToString().ToLowerInvariant(),
                                ["is_public"]   = unit.IsPublic ? "true" : "false",
                            },
                            Properties: new Dictionary<string, string>
                            {
                                ["path"]           = path,
                                ["branch"]         = branch,
                                ["repository"]     = repo,
                                ["unit_name"]      = unit.Name,
                                ["unit_kind"]      = unit.Kind.ToString(),
                                ["unit_namespace"] = unit.Namespace      ?? "",
                                ["unit_container"] = unit.ContainerName  ?? "",
                                ["unit_signature"] = unit.Signature      ?? "",
                            }
                        ));
                    }
                }
                else
                {
                    // Fallback: whole file as a single document
                    documents.Add(new SourceDocument(
                        Id:   $"code-{repo}-{branch}-{path}",
                        Text: content,
                        Tags: new Dictionary<string, string>
                        {
                            ["source_name"] = definition.Name,
                            ["repository"]  = repo,
                            ["file_ext"]    = ext.TrimStart('.'),
                        },
                        Properties: new Dictionary<string, string>
                        {
                            ["path"]       = path,
                            ["branch"]     = branch,
                            ["repository"] = repo,
                        }
                    ));
                }
            }
            catch (Exception ex)
            {
                Console.WriteLine($"[AdoCodeRepoSource] Skipping '{path}': {ex.Message}");
            }
        }

        return documents;
    }

    /// <summary>Simple glob matching supporting * (any chars in segment) and ** (any path segments).</summary>
    private static bool GlobMatch(string pattern, string path)
    {
        // Normalize separators
        pattern = pattern.Replace('\\', '/').TrimStart('/');
        path    = path.Replace('\\', '/').TrimStart('/');

        return GlobMatchCore(pattern.AsSpan(), path.AsSpan());
    }

    private static bool GlobMatchCore(ReadOnlySpan<char> pattern, ReadOnlySpan<char> path)
    {
        while (!pattern.IsEmpty && !path.IsEmpty)
        {
            if (pattern.StartsWith("**/"))
            {
                // ** matches zero or more path segments
                var rest = pattern[3..];
                if (GlobMatchCore(rest, path)) return true;
                var slash = path.IndexOf('/');
                if (slash < 0) return false;
                path = path[(slash + 1)..];
            }
            else if (pattern[0] == '*')
            {
                // * matches within a single segment
                var rest = pattern[1..];
                var slash = path.IndexOf('/');
                var segment = slash >= 0 ? path[..slash] : path;
                for (var i = 0; i <= segment.Length; i++)
                    if (GlobMatchCore(rest, path[i..])) return true;
                return false;
            }
            else if (pattern[0] == path[0])
            {
                pattern = pattern[1..];
                path    = path[1..];
            }
            else
            {
                return false;
            }
        }

        return pattern.IsEmpty && path.IsEmpty;
    }

    private static IReadOnlyList<string> ParsePatterns(string csv)
        => csv.Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
}
