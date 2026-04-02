using Conduit.Rag.Models;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace Conduit.Rag.Sources;

/// <summary>
/// Fetches and indexes content from any HTTP/HTTPS URL.
/// Supports HTML (tag-stripped), JSON (pretty-printed), and plain text.
/// Auth types: none, pat (Basic), bearer, apikey.
/// </summary>
public sealed partial class HttpPageSource(
    SourceDefinition definition,
    IHttpClientFactory httpClientFactory) : ISource
{
    public string Type           => SourceTypes.HttpPage;
    public string CollectionName => CollectionNames.HttpPages;

    public async Task<IReadOnlyList<SourceDocument>> FetchDocumentsAsync(
        CancellationToken ct = default)
    {
        var url         = definition.Config.GetValueOrDefault(ConfigKeys.Url)
                          ?? throw new InvalidOperationException("HttpPageSource requires a 'url' config key.");
        var title       = definition.Config.GetValueOrDefault(ConfigKeys.Title);
        var contentType = definition.Config.GetValueOrDefault(ConfigKeys.ContentType, "auto");

        var client  = httpClientFactory.CreateClient();
        var request = new HttpRequestMessage(HttpMethod.Get, url);
        ApplyAuth(request, definition.Config);

        var response = await client.SendAsync(request, ct);
        response.EnsureSuccessStatusCode();

        var rawContent = await response.Content.ReadAsStringAsync(ct);
        if (string.IsNullOrWhiteSpace(rawContent)) return [];

        var detectedType = ResolveContentType(contentType, response.Content.Headers.ContentType?.MediaType);
        var text = detectedType switch
        {
            "html" => StripHtml(rawContent),
            "json" => PrettyJson(rawContent),
            _      => rawContent
        };

        if (string.IsNullOrWhiteSpace(text)) return [];

        var resolvedTitle = !string.IsNullOrWhiteSpace(title)
            ? title
            : new Uri(url).Host;

        return
        [
            new SourceDocument(
                Id:   $"http-{Fnv1A(url)}",
                Text: text,
                Tags: new Dictionary<string, string>
                {
                    ["source_name"] = definition.Name,
                },
                Properties: new Dictionary<string, string>
                {
                    ["title"] = resolvedTitle,
                    ["url"]   = url,
                }
            )
        ];
    }

    private static string ResolveContentType(string configuredType, string? mediaType)
    {
        if (!string.IsNullOrWhiteSpace(configuredType) && configuredType != "auto")
            return configuredType.ToLowerInvariant();

        if (mediaType is null) return "text";
        if (mediaType.Contains("html", StringComparison.OrdinalIgnoreCase)) return "html";
        if (mediaType.Contains("json", StringComparison.OrdinalIgnoreCase)) return "json";
        return "text";
    }

    private static string StripHtml(string html)
    {
        // Remove scripts and styles with their content
        var noScript = ScriptStyleRegex().Replace(html, " ");
        // Strip remaining tags
        var noTags = HtmlTagRegex().Replace(noScript, " ");
        // Decode common entities
        var decoded = noTags
            .Replace("&amp;",  "&")
            .Replace("&lt;",   "<")
            .Replace("&gt;",   ">")
            .Replace("&quot;", "\"")
            .Replace("&#39;",  "'")
            .Replace("&nbsp;", " ");
        // Collapse whitespace
        return WhitespaceRegex().Replace(decoded, " ").Trim();
    }

    private static string PrettyJson(string json)
    {
        try
        {
            var doc = JsonDocument.Parse(json);
            return JsonSerializer.Serialize(doc, new JsonSerializerOptions { WriteIndented = true });
        }
        catch
        {
            return json;
        }
    }

    private static void ApplyAuth(HttpRequestMessage request, IReadOnlyDictionary<string, string> config)
    {
        var authType = config.GetValueOrDefault(ConfigKeys.AuthType, "none").ToLowerInvariant();
        switch (authType)
        {
            case "pat" when config.TryGetValue(ConfigKeys.Pat, out var pat) && !string.IsNullOrEmpty(pat):
                var encoded = Convert.ToBase64String(Encoding.ASCII.GetBytes($":{pat}"));
                request.Headers.Authorization = new AuthenticationHeaderValue("Basic", encoded);
                break;

            case "bearer" when config.TryGetValue(ConfigKeys.Token, out var token) && !string.IsNullOrEmpty(token):
                request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);
                break;

            case "apikey"
                when config.TryGetValue(ConfigKeys.ApiKeyHeader, out var header) && !string.IsNullOrEmpty(header)
                  && config.TryGetValue(ConfigKeys.ApiKeyValue, out var value)   && !string.IsNullOrEmpty(value):
                request.Headers.TryAddWithoutValidation(header, value);
                break;
        }
    }

    /// <summary>FNV-1a 32-bit hash — stable, URL-safe document ID generation.</summary>
    private static string Fnv1A(string input)
    {
        const uint offsetBasis = 2166136261;
        const uint prime       = 16777619;

        uint hash = offsetBasis;
        foreach (var b in Encoding.UTF8.GetBytes(input))
        {
            hash ^= b;
            hash *= prime;
        }
        return hash.ToString("x8");
    }

    [GeneratedRegex(@"<(script|style)[^>]*>.*?</(script|style)>",
        RegexOptions.IgnoreCase | RegexOptions.Singleline)]
    private static partial Regex ScriptStyleRegex();

    [GeneratedRegex(@"<[^>]+>")]
    private static partial Regex HtmlTagRegex();

    [GeneratedRegex(@"\s{2,}")]
    private static partial Regex WhitespaceRegex();
}
