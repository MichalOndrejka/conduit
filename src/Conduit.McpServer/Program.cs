using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using Conduit.Rag.Services;
using Conduit.Rag.Sources;
using Microsoft.Extensions.AI;
using OllamaSharp;
using OpenAI;
using Qdrant.Client;
using System.ClientModel;

var builder = WebApplication.CreateBuilder(args);
var config  = builder.Configuration;

// ── Configuration ────────────────────────────────────────────────────────────

var qdrantHost     = config["QDRANT_HOST"]     ?? config["Qdrant:Host"]     ?? "localhost";
var qdrantGrpcPort = int.Parse(config["QDRANT_GRPC_PORT"] ?? config["Qdrant:GrpcPort"] ?? "6334");

var embeddingProvider = config["Embedding:Provider"] ?? "openai";
var embeddingModel    = config["Embedding:Model"]    ?? "text-embedding-3-small";
var embeddingApiKeyEnvVar = config["Embedding:ApiKeyEnvVar"] ?? "OPENAI_API_KEY";
var embeddingApiKey       = Environment.GetEnvironmentVariable(embeddingApiKeyEnvVar) ?? "";
var embeddingBaseUrl  = config["Embedding:BaseUrl"]  ?? "";
var embeddingDim      = int.Parse(config["Embedding:Dimensions"] ?? "1536");

var chunkingOptions = new ChunkingOptions
{
    MaxChunkSize = int.Parse(config["Chunking:MaxChunkSize"] ?? "2000"),
    Overlap      = int.Parse(config["Chunking:Overlap"]      ?? "200")
};

var sourcesFilePath = config["SourcesFilePath"] ?? "conduit-sources.json";
// Resolve relative paths against content root
if (!Path.IsPathRooted(sourcesFilePath))
    sourcesFilePath = Path.Combine(builder.Environment.ContentRootPath, sourcesFilePath);

var fingerprintPath = Path.Combine(builder.Environment.ContentRootPath, "conduit-embedding.json");

// ── Core Infrastructure ───────────────────────────────────────────────────────

builder.Services.AddSingleton(_ => new QdrantClient(qdrantHost, qdrantGrpcPort));
builder.Services.AddSingleton<QdrantHealthStatus>();

var ollamaUri         = Uri.TryCreate(string.IsNullOrEmpty(embeddingBaseUrl) ? "http://localhost:11434" : embeddingBaseUrl, UriKind.Absolute, out var u1) ? u1 : new Uri("http://localhost:11434");
var compatibleBaseUri = Uri.TryCreate(embeddingBaseUrl, UriKind.Absolute, out var u2) ? u2 : new Uri("http://localhost:11434");

builder.Services.AddSingleton<IEmbeddingGenerator<string, Embedding<float>>>(_ =>
    embeddingProvider switch
    {
        "ollama" => (IEmbeddingGenerator<string, Embedding<float>>)new OllamaApiClient(ollamaUri, embeddingModel),
        "openai-compatible" => new OpenAIClient(
                new ApiKeyCredential(string.IsNullOrEmpty(embeddingApiKey) ? "placeholder" : embeddingApiKey),
                new OpenAIClientOptions { Endpoint = compatibleBaseUri })
            .GetEmbeddingClient(embeddingModel)
            .AsIEmbeddingGenerator(),
        _ => new OpenAIClient(new ApiKeyCredential(string.IsNullOrEmpty(embeddingApiKey) ? "placeholder" : embeddingApiKey))
                .GetEmbeddingClient(embeddingModel)
                .AsIEmbeddingGenerator()
    });

// ── Conduit.Rag Services ─────────────────────────────────────────────────────

builder.Services.AddSingleton(chunkingOptions);
builder.Services.AddSingleton<IEmbeddingService, EmbeddingService>();
builder.Services.AddSingleton<IQdrantFilterFactory, QdrantFilterFactory>();
builder.Services.AddSingleton<ITextChunker, TextChunker>();
builder.Services.AddSingleton<IVectorStore, QdrantVectorStore>();
builder.Services.AddSingleton<IDocumentIndexer, DocumentIndexer>();
builder.Services.AddSingleton<ISearchService, SearchService>();
builder.Services.AddSingleton<ISourceConfigStore>(_ => new JsonSourceConfigStore(sourcesFilePath));
builder.Services.AddSingleton<ISyncService, SyncService>();
builder.Services.AddHttpClient<AdoClient>(); // registers IHttpClientFactory + typed client
builder.Services.AddSingleton<IAdoClient>(sp => sp.GetRequiredService<AdoClient>());
builder.Services.AddSingleton<ICodeParser, CSharpParser>();
builder.Services.AddSingleton<ICodeParser, TypeScriptParser>();
builder.Services.AddSingleton<ICodeParser, GoParser>();
builder.Services.AddSingleton<ICodeParser, PowerShellParser>();
builder.Services.AddSingleton<ICodeParser, MarkdownParser>();
builder.Services.AddSingleton<ICodeParser, GenericSectionParser>();
builder.Services.AddSingleton<CodeParserRegistry>();
builder.Services.AddSingleton<SourceFactory>();

builder.Services.AddHostedService(sp =>
    new QdrantBootstrapper(
        sp.GetRequiredService<QdrantClient>(),
        embeddingDim,
        embeddingModel,
        fingerprintPath,
        sp.GetRequiredService<QdrantHealthStatus>()));

// ── MCP Server ───────────────────────────────────────────────────────────────

builder.Services
    .AddMcpServer()
    .WithHttpTransport()
    .WithToolsFromAssembly();

// ── Razor Pages (Config UI) ───────────────────────────────────────────────────

builder.Services.AddRazorPages();

// ── Build & Pipeline ─────────────────────────────────────────────────────────

var app = builder.Build();

app.UseStaticFiles();
app.UseRouting();
app.MapRazorPages();
app.MapMcp("/mcp");

app.Run();
