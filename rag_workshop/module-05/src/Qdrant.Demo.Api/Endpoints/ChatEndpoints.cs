using Microsoft.AspNetCore.Mvc;
using Microsoft.Extensions.AI;
using Qdrant.Client;
using Qdrant.Demo.Api.Extensions;
using Qdrant.Demo.Api.Models;
using Qdrant.Demo.Api.Services;
using static Qdrant.Demo.Api.Models.PayloadKeys;
using ChatResponse = Qdrant.Demo.Api.Models.ChatResponse;

namespace Qdrant.Demo.Api.Endpoints;

public static class ChatEndpoints
{
    private const string DefaultSystemPrompt =
        """
        You are a helpful assistant. Answer the user's question based **only** on
        the provided context documents. If the context does not contain enough
        information to answer, say so clearly — do not make up facts.
        """;

    public static WebApplication MapChatEndpoints(this WebApplication app, string collectionName)
    {
        // ─────────────────────────────────────────────────────
        // POST /chat — retrieve + generate (basic RAG)
        // ─────────────────────────────────────────────────────
        app.MapPost("/chat", async (
            [FromBody] ChatRequest req,
            QdrantClient qdrant,
            IEmbeddingService embeddings,
            IChatClient chatClient,
            CancellationToken ct) =>
        {
            if (string.IsNullOrWhiteSpace(req.Question))
                return Results.BadRequest("Question is required and cannot be empty.");

            try
            {
                // 1. Embed the user's question
                var vector = await embeddings.EmbedAsync(req.Question, ct);

                // 2. Retrieve the top-K most similar documents (no filter yet)
                var hits = await qdrant.SearchAsync(
                    collectionName: collectionName,
                    vector: vector,
                    limit: (ulong)req.K,
                    payloadSelector: true,
                    cancellationToken: ct);

                // 3. Build sources list and context string
                List<ChatSource> sources = [];
                List<string> contextParts = [];

                for (var i = 0; i < hits.Count; i++)
                {
                    var hit = hits[i];
                    var id = hit.Id?.Uuid ?? hit.Id?.Num.ToString() ?? "?";
                    var text = hit.Payload.TryGetValue(Text, out var v)
                        ? v.StringValue
                        : string.Empty;

                    sources.Add(new ChatSource(id, hit.Score, text));
                    contextParts.Add($"[{i + 1}] {text}");
                }

                var context = string.Join("\n\n", contextParts);

                // 4. Call the chat-completion model
                List<ChatMessage> messages =
                [
                    new ChatMessage(ChatRole.System, DefaultSystemPrompt),
                    new ChatMessage(ChatRole.User,
                        $"""
                        Context:
                        {context}

                        Question: {req.Question}
                        """)
                ];

                var response = await chatClient.GetResponseAsync(messages, cancellationToken: ct);
                var answer = response.Text;

                return Results.Ok(new ChatResponse(answer, sources));
            }
            catch (Exception ex)
            {
                Console.WriteLine($"[chat] Error: {ex.Message}");
                return Results.Problem(
                    detail: ex.Message, statusCode: 500, title: "Chat failed");
            }
        });

        return app;
    }
}
