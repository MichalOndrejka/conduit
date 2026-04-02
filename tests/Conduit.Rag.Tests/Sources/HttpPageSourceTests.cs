using Conduit.Rag.Models;
using Conduit.Rag.Sources;
using Moq;
using NUnit.Framework;
using System.Net;
using System.Net.Http.Headers;

namespace Conduit.Rag.Tests.Sources;

[TestFixture]
public class HttpPageSourceTests
{
    // ─── Content type detection ───────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_HtmlContentType_StripsTagsFromResponse()
    {
        var source = MakeSource(url: "https://example.com/page",
            responseBody: "<html><body><h1>Hello</h1><p>World</p></body></html>",
            mediaType: "text/html");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Text, Does.Contain("Hello"));
        Assert.That(docs[0].Text, Does.Not.Contain("<h1>"));
    }

    [Test]
    public async Task FetchDocumentsAsync_JsonContentType_PrettyPrintsJson()
    {
        var source = MakeSource(url: "https://example.com/api",
            responseBody: "{\"key\":\"value\"}",
            mediaType: "application/json");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Text, Does.Contain("\"key\""));
        Assert.That(docs[0].Text, Does.Contain("\"value\""));
    }

    [Test]
    public async Task FetchDocumentsAsync_ManualHtmlOverride_StripsTagsRegardlessOfHeader()
    {
        var source = MakeSource(url: "https://example.com/page",
            responseBody: "<p>Text</p>",
            mediaType: "text/plain",
            contentType: "html");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Text, Does.Not.Contain("<p>"));
        Assert.That(docs[0].Text, Does.Contain("Text"));
    }

    [Test]
    public async Task FetchDocumentsAsync_TextContentType_ReturnsRawText()
    {
        const string body = "Plain text content.";
        var source = MakeSource(url: "https://example.com/text",
            responseBody: body,
            mediaType: "text/plain");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Text, Is.EqualTo(body));
    }

    [Test]
    public async Task FetchDocumentsAsync_ScriptTagsInHtml_AreRemoved()
    {
        const string html = "<html><body><script>alert('xss')</script><p>Safe</p></body></html>";
        var source = MakeSource(url: "https://example.com/page",
            responseBody: html,
            mediaType: "text/html");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Text, Does.Not.Contain("alert"));
        Assert.That(docs[0].Text, Does.Contain("Safe"));
    }

    // ─── Document ID stability ────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_SameUrl_ProducesSameId()
    {
        var source1 = MakeSource(url: "https://example.com/page", responseBody: "v1", mediaType: "text/plain");
        var source2 = MakeSource(url: "https://example.com/page", responseBody: "v2", mediaType: "text/plain");

        var docs1 = await source1.FetchDocumentsAsync();
        var docs2 = await source2.FetchDocumentsAsync();

        Assert.That(docs1[0].Id, Is.EqualTo(docs2[0].Id));
    }

    [Test]
    public async Task FetchDocumentsAsync_DifferentUrls_ProduceDifferentIds()
    {
        var source1 = MakeSource(url: "https://example.com/a", responseBody: "x", mediaType: "text/plain");
        var source2 = MakeSource(url: "https://example.com/b", responseBody: "x", mediaType: "text/plain");

        var docs1 = await source1.FetchDocumentsAsync();
        var docs2 = await source2.FetchDocumentsAsync();

        Assert.That(docs1[0].Id, Is.Not.EqualTo(docs2[0].Id));
    }

    // ─── Title resolution ─────────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_TitleFromConfig_UsesConfigTitle()
    {
        var source = MakeSource(url: "https://example.com/page",
            responseBody: "content", mediaType: "text/plain",
            title: "My Custom Title");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Properties["title"], Is.EqualTo("My Custom Title"));
    }

    [Test]
    public async Task FetchDocumentsAsync_NoTitleInConfig_UsesHostname()
    {
        var source = MakeSource(url: "https://docs.example.com/page",
            responseBody: "content", mediaType: "text/plain");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Properties["title"], Is.EqualTo("docs.example.com"));
    }

    // ─── Properties and tags ──────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_Properties_ContainUrl()
    {
        var source = MakeSource(url: "https://example.com/page",
            responseBody: "content", mediaType: "text/plain");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Properties["url"], Is.EqualTo("https://example.com/page"));
    }

    [Test]
    public async Task FetchDocumentsAsync_Tags_ContainSourceName()
    {
        var source = MakeSource(url: "https://example.com/page",
            responseBody: "content", mediaType: "text/plain");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs[0].Tags["source_name"], Is.EqualTo("Test Page"));
    }

    // ─── Collection metadata ──────────────────────────────────────

    [Test]
    public void CollectionName_IsHttpPagesCollection()
    {
        var source = MakeSource(url: "https://example.com", responseBody: "", mediaType: "text/plain");
        Assert.That(source.CollectionName, Is.EqualTo(CollectionNames.HttpPages));
    }

    // ─── Empty response ───────────────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_EmptyBody_ReturnsEmpty()
    {
        var source = MakeSource(url: "https://example.com/page",
            responseBody: "   ", mediaType: "text/plain");

        var docs = await source.FetchDocumentsAsync();

        Assert.That(docs, Is.Empty);
    }

    // ─── Auth header application ──────────────────────────────────

    [Test]
    public async Task FetchDocumentsAsync_BearerAuth_SendsAuthorizationHeader()
    {
        HttpRequestMessage? captured = null;
        var source = MakeSource(url: "https://example.com", responseBody: "ok", mediaType: "text/plain",
            authType: "bearer", token: "my-token",
            captureRequest: req => captured = req);

        await source.FetchDocumentsAsync();

        Assert.That(captured?.Headers.Authorization?.Scheme, Is.EqualTo("Bearer"));
        Assert.That(captured?.Headers.Authorization?.Parameter, Is.EqualTo("my-token"));
    }

    [Test]
    public async Task FetchDocumentsAsync_PatAuth_SendsBasicAuthHeader()
    {
        HttpRequestMessage? captured = null;
        var source = MakeSource(url: "https://example.com", responseBody: "ok", mediaType: "text/plain",
            authType: "pat", pat: "secret-pat",
            captureRequest: req => captured = req);

        await source.FetchDocumentsAsync();

        Assert.That(captured?.Headers.Authorization?.Scheme, Is.EqualTo("Basic"));
        Assert.That(captured?.Headers.Authorization?.Parameter, Is.Not.Null);
    }

    // ─── Helpers ──────────────────────────────────────────────────

    private static HttpPageSource MakeSource(
        string url,
        string responseBody,
        string mediaType,
        string? title       = null,
        string? contentType = null,
        string? authType    = null,
        string? pat         = null,
        string? token       = null,
        string? apiKeyHeader = null,
        string? apiKeyValue  = null,
        Action<HttpRequestMessage>? captureRequest = null)
    {
        var handler = new CapturingHandler(responseBody, mediaType, captureRequest);
        var httpClient = new HttpClient(handler);
        var factory = new Mock<IHttpClientFactory>();
        factory.Setup(f => f.CreateClient(It.IsAny<string>())).Returns(httpClient);

        var config = new Dictionary<string, string> { [ConfigKeys.Url] = url };
        if (title       is not null) config[ConfigKeys.Title]        = title;
        if (contentType is not null) config[ConfigKeys.ContentType]  = contentType;
        if (authType    is not null) config[ConfigKeys.AuthType]     = authType;
        if (pat         is not null) config[ConfigKeys.Pat]          = pat;
        if (token       is not null) config[ConfigKeys.Token]        = token;
        if (apiKeyHeader is not null) config[ConfigKeys.ApiKeyHeader] = apiKeyHeader;
        if (apiKeyValue  is not null) config[ConfigKeys.ApiKeyValue]  = apiKeyValue;

        var def = new SourceDefinition
        {
            Id     = "http1",
            Name   = "Test Page",
            Type   = SourceTypes.HttpPage,
            Config = config
        };

        return new HttpPageSource(def, factory.Object);
    }

    private sealed class CapturingHandler(
        string body,
        string mediaType,
        Action<HttpRequestMessage>? capture) : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken ct)
        {
            capture?.Invoke(request);
            var response = new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StringContent(body)
            };
            response.Content.Headers.ContentType = new MediaTypeHeaderValue(mediaType);
            return Task.FromResult(response);
        }
    }
}
