using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Parsing;
using System.Text.Json;

namespace Conduit.Rag.Sources;

/// <summary>
/// Fetches all Markdown pages from an Azure DevOps Wiki and indexes them as
/// heading-level sections using MarkdownParser.
/// <para>
/// ADO wikis are backed by a Git repository — the wikis API returns a
/// <c>repositoryId</c> which is then fed directly into the existing
/// <see cref="IAdoClient.GetFileTreeAsync"/> / <see cref="IAdoClient.GetFileContentAsync"/>
/// methods, reusing all of the code-repo fetch and parse logic.
/// </para>
/// </summary>
public sealed class AdoWikiSource(
    SourceDefinition definition,
    IAdoClient ado,
    CodeParserRegistry parserRegistry) : ISource
{
    public string Type           => SourceTypes.AdoWiki;
    public string CollectionName => CollectionNames.AdoWiki;

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(
        CancellationToken ct = default)
    {
        var conn       = AdoConnectionConfig.From(definition.Config);
        var wikiName   = definition.Config.GetValueOrDefault(ConfigKeys.WikiName);
        var pathFilter = NormalizePath(definition.Config.GetValueOrDefault(ConfigKeys.PathFilter));

        // ── 1. Resolve the wiki's backing Git repository ──────────────────────
        var wikis = await ado.GetWikisAsync(conn, ct);
        if (wikis.Count == 0)
            throw new InvalidOperationException($"No wikis found at {conn.BaseUrl}.");

        JsonElement wiki;
        if (string.IsNullOrWhiteSpace(wikiName))
        {
            wiki = wikis[0];
        }
        else
        {
            wiki = wikis.FirstOrDefault(w =>
                w.TryGetProperty("name", out var n) &&
                string.Equals(n.GetString(), wikiName, StringComparison.OrdinalIgnoreCase));

            if (wiki.ValueKind == JsonValueKind.Undefined)
            {
                var available = string.Join(", ", wikis
                    .Select(w => w.TryGetProperty("name", out var n) ? n.GetString() : "?"));
                throw new InvalidOperationException(
                    $"Wiki '{wikiName}' not found. Available: {available}");
            }
        }

        var repositoryId = wiki.GetProperty("repositoryId").GetString()
            ?? throw new InvalidOperationException("Wiki has no repositoryId.");
        var resolvedWikiName = wiki.TryGetProperty("name", out var nameEl)
            ? nameEl.GetString() ?? string.Empty
            : string.Empty;

        // Wiki repos use "wikiMaster" as the default branch name
        const string branch = "wikiMaster";

        // ── 2. Get all .md file paths under the path filter ───────────────────
        var allPaths = await ado.GetFileTreeAsync(conn, repositoryId, branch, ct: ct);
        var mdPaths = allPaths
            .Where(p => p.EndsWith(".md", StringComparison.OrdinalIgnoreCase)
                     && p.StartsWith(pathFilter, StringComparison.OrdinalIgnoreCase))
            .ToList();

        // ── 3. Fetch and parse each file ──────────────────────────────────────
        var parser    = parserRegistry.Resolve(".md");
        var documents = new List<SourceDocument>(mdPaths.Count * 2);

        foreach (var path in mdPaths)
        {
            ct.ThrowIfCancellationRequested();
            try
            {
                var content = await ado.GetFileContentAsync(conn, repositoryId, branch, path, ct);
                if (string.IsNullOrWhiteSpace(content)) continue;

                var units = parser?.Parse(content, path) ?? [];

                if (units.Count > 0)
                {
                    foreach (var unit in units)
                    {
                        documents.Add(new SourceDocument(
                            Id:   $"wiki-{repositoryId}-{path}-{unit.ToIdSlug()}",
                            Text: unit.EnrichedText,
                            Tags: new Dictionary<string, string>
                            {
                                ["source_name"] = definition.Name,
                                ["wiki_name"]   = resolvedWikiName,
                                ["section"]     = unit.Name,
                            },
                            Properties: new Dictionary<string, string>
                            {
                                ["title"]      = unit.Name,
                                ["path"]       = path,
                                ["wiki_name"]  = resolvedWikiName,
                                ["repository"] = repositoryId,
                            }
                        ));
                    }
                }
                else
                {
                    // No headings — index the whole file as one document
                    var title = Path.GetFileNameWithoutExtension(path).Replace('-', ' ');
                    documents.Add(new SourceDocument(
                        Id:   $"wiki-{repositoryId}-{path}",
                        Text: content,
                        Tags: new Dictionary<string, string>
                        {
                            ["source_name"] = definition.Name,
                            ["wiki_name"]   = resolvedWikiName,
                        },
                        Properties: new Dictionary<string, string>
                        {
                            ["title"]      = title,
                            ["path"]       = path,
                            ["wiki_name"]  = resolvedWikiName,
                            ["repository"] = repositoryId,
                        }
                    ));
                }
            }
            catch (Exception ex)
            {
                Console.WriteLine($"[AdoWikiSource] Skipping '{path}': {ex.Message}");
            }
        }

        return documents;
    }

    private static string NormalizePath(string? p)
    {
        p = (p ?? "/").Trim();
        if (!p.StartsWith('/')) p = "/" + p;
        if (p.Length > 1 && p.EndsWith('/')) p = p.TrimEnd('/');
        return p;
    }
}
