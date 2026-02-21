using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using Conduit.Rag.Services;
using Conduit.Rag.Sources;
using Microsoft.Extensions.AI;
using OpenAI;
using Qdrant.Client;

var builder = WebApplication.CreateBuilder(args);
var config  = builder.Configuration;

// ── Configuration ────────────────────────────────────────────────────────────

var qdrantHost    = config["QDRANT_HOST"]     ?? config["Qdrant:Host"]     ?? "localhost";
var qdrantGrpcPort = int.Parse(config["QDRANT_GRPC_PORT"] ?? config["Qdrant:GrpcPort"] ?? "6334");
var embeddingDim  = int.Parse(config["Qdrant:EmbeddingDim"] ?? "1536");

var openAiApiKey      = config["OPENAI_API_KEY"] ?? config["OpenAI:ApiKey"]
    ?? throw new InvalidOperationException("OpenAI API key is required. Set 'OpenAI:ApiKey' in appsettings.json or the OPENAI_API_KEY environment variable.");
var embeddingModel = config["OpenAI:EmbeddingModel"] ?? "text-embedding-3-small";

var chunkingOptions = new ChunkingOptions
{
    MaxChunkSize = int.Parse(config["Chunking:MaxChunkSize"] ?? "2000"),
    Overlap      = int.Parse(config["Chunking:Overlap"]      ?? "200")
};

var sourcesFilePath = config["SourcesFilePath"] ?? "conduit-sources.json";
// Resolve relative paths against content root
if (!Path.IsPathRooted(sourcesFilePath))
    sourcesFilePath = Path.Combine(builder.Environment.ContentRootPath, sourcesFilePath);

// ── Core Infrastructure ───────────────────────────────────────────────────────

builder.Services.AddSingleton(_ => new QdrantClient(qdrantHost, qdrantGrpcPort));

builder.Services.AddSingleton<IEmbeddingGenerator<string, Embedding<float>>>(_ =>
    new OpenAIClient(openAiApiKey)
        .GetEmbeddingClient(embeddingModel)
        .AsIEmbeddingGenerator());

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
builder.Services.AddHttpClient<AdoClient>();
builder.Services.AddSingleton<IAdoClient>(sp => sp.GetRequiredService<AdoClient>());
builder.Services.AddSingleton<SourceFactory>();

builder.Services.AddHostedService(sp =>
    new QdrantBootstrapper(
        sp.GetRequiredService<QdrantClient>(),
        embeddingDim));

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
