using Microsoft.Extensions.AI;
using OpenAI;
using Qdrant.Client;
using Qdrant.Demo.Api.Endpoints;
using Qdrant.Demo.Api.Services;

var builder = WebApplication.CreateBuilder(args);

// ---- configuration (appsettings.json → env vars override) ----
var config = builder.Configuration;

var qdrantHost     = config["QDRANT_HOST"]       ?? config["Qdrant:Host"]       ?? "localhost";
var qdrantHttpPort = int.Parse(config["QDRANT_HTTP_PORT"] ?? config["Qdrant:HttpPort"] ?? "6333");
var qdrantGrpcPort = int.Parse(config["QDRANT_GRPC_PORT"] ?? config["Qdrant:GrpcPort"] ?? "6334");
var collectionName = config["QDRANT_COLLECTION"] ?? config["Qdrant:Collection"] ?? "documents";
var embeddingDim   = int.Parse(config["EMBEDDING_DIM"]    ?? config["Qdrant:EmbeddingDim"] ?? "1536");
var embeddingModel = config["EMBEDDING_MODEL"] ?? config["OpenAI:EmbeddingModel"] ?? "text-embedding-3-small";
var chatModel      = config["CHAT_MODEL"]      ?? config["OpenAI:ChatModel"]      ?? "gpt-4o-mini";
var openAiApiKey   = config["OPENAI_API_KEY"]  ?? config["OpenAI:ApiKey"] ?? throw new InvalidOperationException("Set the OPENAI_API_KEY environment variable or OpenAI:ApiKey in appsettings.json.");

// ---- service registration ----
builder.Services.AddEndpointsApiExplorer();
builder.Services.AddSwaggerGen(opts =>
{
    opts.SwaggerDoc("v1", new() { Title = "Qdrant.Demo API", Version = "v1" });
});

builder.Services.AddSingleton(_ => new QdrantClient(qdrantHost, qdrantGrpcPort));
var openAi = new OpenAIClient(openAiApiKey);
builder.Services.AddSingleton<IEmbeddingGenerator<string, Embedding<float>>>(
    openAi.GetEmbeddingClient(embeddingModel).AsIEmbeddingGenerator());
builder.Services.AddSingleton<IChatClient>(
    openAi.GetChatClient(chatModel).AsIChatClient());
builder.Services.AddSingleton<IEmbeddingService, EmbeddingService>();
builder.Services.AddSingleton<IQdrantFilterFactory, QdrantFilterFactory>();

builder.Services.AddHostedService(sp =>
    new QdrantBootstrapper(
        sp.GetRequiredService<QdrantClient>(),
        collectionName,
        embeddingDim));

builder.Services.AddSingleton<IDocumentIndexer>(sp =>
    new DocumentIndexer(
        sp.GetRequiredService<QdrantClient>(),
        sp.GetRequiredService<IEmbeddingService>(),
        collectionName));

var app = builder.Build();

// ---- Swagger (always-on — this is a workshop template) ----
app.UseSwagger();
app.UseSwaggerUI();

// ---- endpoints ----
app.MapGet("/", () => Results.Ok(new
{
    service = "Qdrant.Demo.Api",
    qdrant = new
    {
        host = qdrantHost,
        http = qdrantHttpPort,
        grpc = qdrantGrpcPort,
        collection = collectionName,
        embeddingDim
    },
    embeddingModel,
    chatModel
}));

app.MapGet("/health", () => Results.Ok("healthy"))
    .ExcludeFromDescription();

app.MapDocumentEndpoints();
app.MapSearchEndpoints(collectionName);
app.MapChatEndpoints(collectionName);

app.Run();
