using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Mvc.RazorPages;
using Qdrant.Client;
using Qdrant.Client.Grpc;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace Conduit.McpServer.Pages;

public sealed class SettingsModel(
    IWebHostEnvironment env,
    QdrantHealthStatus qdrantHealth,
    QdrantClient qdrantClient,
    ISourceConfigStore sourceStore) : PageModel
{
    // Embedding
    [BindProperty] public string Provider   { get; set; } = "openai";
    [BindProperty] public string Model      { get; set; } = "text-embedding-3-small";
    [BindProperty] public string ApiKeyEnvVar { get; set; } = "OPENAI_API_KEY";
    [BindProperty] public string BaseUrl    { get; set; } = "";
    [BindProperty] public int    Dimensions { get; set; } = 1536;

    // Qdrant
    [BindProperty] public string QdrantHost     { get; set; } = "localhost";
    [BindProperty] public int    QdrantGrpcPort { get; set; } = 6334;

    [TempData] public string? StatusMessage { get; set; }
    [TempData] public bool    WasDropped    { get; set; }

    public bool    QdrantReady   => qdrantHealth.IsReady;
    public string? QdrantError   => qdrantHealth.Error;
    public string  ConfigFilePath => AppSettingsPath;

    public void OnGet() => LoadFromFile();

    public async Task<IActionResult> OnPostAsync()
    {
        if (!ModelState.IsValid) return Page();

        if (!string.IsNullOrEmpty(BaseUrl) &&
            !Uri.TryCreate(BaseUrl.Trim(), UriKind.Absolute, out _))
        {
            ModelState.AddModelError(nameof(BaseUrl), "Base URL must be a valid absolute URL.");
            return Page();
        }

        var path = AppSettingsPath;
        var root = ReadJson(path);

        // Compare against what's on disk (IConfiguration is a startup snapshot and doesn't update)
        var oldEmb      = root["Embedding"] as JsonObject;
        var oldModel    = oldEmb?["Model"]?.GetValue<string>()      ?? "text-embedding-3-small";
        var oldDim      = oldEmb?["Dimensions"]?.GetValue<int>()    ?? 1536;
        var oldProvider = oldEmb?["Provider"]?.GetValue<string>()   ?? "openai";
        var oldBaseUrl  = oldEmb?["BaseUrl"]?.GetValue<string>()    ?? "";
        var embeddingChanged = Model.Trim()       != oldModel    ||
                               Dimensions         != oldDim      ||
                               Provider.Trim()    != oldProvider ||
                               BaseUrl.Trim()     != oldBaseUrl;

        // Write new settings
        var embedding = root["Embedding"] as JsonObject ?? new JsonObject();
        embedding["Provider"]   = Provider.Trim();
        embedding["Model"]      = Model.Trim();
        embedding["ApiKeyEnvVar"] = ApiKeyEnvVar.Trim();
        embedding["BaseUrl"]    = BaseUrl.Trim();
        embedding["Dimensions"] = Dimensions;
        root["Embedding"]       = embedding;

        var qdrant = root["Qdrant"] as JsonObject ?? new JsonObject();
        qdrant["Host"]     = QdrantHost.Trim();
        qdrant["GrpcPort"] = QdrantGrpcPort;
        root["Qdrant"]     = qdrant;

        try
        {
            System.IO.File.WriteAllText(path,
                root.ToJsonString(new JsonSerializerOptions { WriteIndented = true }));
        }
        catch (Exception ex)
        {
            ModelState.AddModelError(string.Empty, $"Failed to save settings to '{path}': {ex.Message}");
            return Page();
        }

        if (embeddingChanged)
        {
            await sourceStore.ResetAllSyncStatusAsync("needs-reindex");

            if (qdrantHealth.IsReady)
            {
                await DropAndRecreateCollectionsAsync();
                WriteFingerprint();
            }

            WasDropped = true;
        }

        StatusMessage = "saved";
        return RedirectToPage();
    }

    private void LoadFromFile()
    {
        var root = ReadJson(AppSettingsPath);

        var emb    = root["Embedding"] as JsonObject;
        Provider   = emb?["Provider"]?.GetValue<string>()   ?? "openai";
        Model      = emb?["Model"]?.GetValue<string>()      ?? "text-embedding-3-small";
        ApiKeyEnvVar = emb?["ApiKeyEnvVar"]?.GetValue<string>() ?? "OPENAI_API_KEY";
        BaseUrl    = emb?["BaseUrl"]?.GetValue<string>()    ?? "";
        Dimensions = emb?["Dimensions"]?.GetValue<int>()    ?? 1536;

        var qdrant     = root["Qdrant"] as JsonObject;
        QdrantHost     = qdrant?["Host"]?.GetValue<string>()  ?? "localhost";
        QdrantGrpcPort = qdrant?["GrpcPort"]?.GetValue<int>() ?? 6334;
    }

    private async Task DropAndRecreateCollectionsAsync()
    {
        var existing = await qdrantClient.ListCollectionsAsync();
        foreach (var name in CollectionNames.All.Where(n => existing.Contains(n)))
            await qdrantClient.DeleteCollectionAsync(name);

        foreach (var name in CollectionNames.All)
            await qdrantClient.CreateCollectionAsync(
                collectionName: name,
                vectorsConfig: new VectorParams { Size = (uint)Dimensions, Distance = Distance.Cosine });
    }

    private void WriteFingerprint()
    {
        var json = JsonSerializer.Serialize(
            new { model = Model.Trim(), dimensions = Dimensions },
            new JsonSerializerOptions { WriteIndented = true });
        System.IO.File.WriteAllText(
            Path.Combine(env.ContentRootPath, "conduit-embedding.json"), json);
    }

    private string AppSettingsPath => Path.Combine(env.ContentRootPath, "appsettings.json");

    private static JsonObject ReadJson(string path)
    {
        if (!System.IO.File.Exists(path)) return new JsonObject();
        var text = System.IO.File.ReadAllText(path);
        return JsonNode.Parse(text,
                   documentOptions: new JsonDocumentOptions { CommentHandling = JsonCommentHandling.Skip })
               as JsonObject ?? new JsonObject();
    }
}
