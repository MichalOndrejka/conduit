using Conduit.Rag.Ado;
using Conduit.Rag.Models;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Ado;

[TestFixture]
public class AdoConnectionConfigTests
{
    // ── BaseUrl resolution ───────────────────────────────────────────────────

    [Test]
    public void From_WithBaseUrl_UsesItDirectly()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]  = "https://ado.company.com/Collection/MyProject",
            [ConfigKeys.AuthType] = "none"
        });

        Assert.That(config.BaseUrl, Is.EqualTo("https://ado.company.com/Collection/MyProject"));
    }

    [Test]
    public void From_WithBaseUrl_StripsTrailingSlash()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]  = "https://ado.company.com/Collection/MyProject/",
            [ConfigKeys.AuthType] = "none"
        });

        Assert.That(config.BaseUrl, Does.Not.EndWith("/"));
    }

    [Test]
    public void From_WithOrganizationAndProject_ConstructsCloudUrl()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.Organization] = "myorg",
            [ConfigKeys.Project]      = "MyProject",
            [ConfigKeys.Pat]          = "token"
        });

        Assert.That(config.BaseUrl, Is.EqualTo("https://dev.azure.com/myorg/MyProject"));
    }

    [Test]
    public void From_WithBaseUrlAndOrganization_PrefersBaseUrl()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]      = "https://ado.company.com/Col/Proj",
            [ConfigKeys.Organization] = "myorg",
            [ConfigKeys.Project]      = "MyProject",
            [ConfigKeys.AuthType]     = "none"
        });

        Assert.That(config.BaseUrl, Is.EqualTo("https://ado.company.com/Col/Proj"));
    }

    [Test]
    public void From_WithoutBaseUrlOrOrganization_Throws()
    {
        Assert.Throws<InvalidOperationException>(() =>
            MakeConfig(new() { [ConfigKeys.Pat] = "token" }));
    }

    // ── AuthType resolution ──────────────────────────────────────────────────

    [Test]
    public void From_WithExplicitAuthType_UsesIt()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]  = "https://ado.company.com/Col/Proj",
            [ConfigKeys.AuthType] = "bearer",
            [ConfigKeys.Token]    = "my-token"
        });

        Assert.That(config.AuthType, Is.EqualTo("bearer"));
        Assert.That(config.Token,    Is.EqualTo("my-token"));
    }

    [Test]
    public void From_WithPatKeyAndNoAuthType_DefaultsToPat()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.Organization] = "org",
            [ConfigKeys.Project]      = "proj",
            [ConfigKeys.Pat]          = "my-pat"
        });

        Assert.That(config.AuthType, Is.EqualTo("pat"));
        Assert.That(config.Pat,      Is.EqualTo("my-pat"));
    }

    [Test]
    public void From_WithNoPatAndNoAuthType_DefaultsToNone()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl] = "https://ado.company.com/Col/Proj"
        });

        Assert.That(config.AuthType, Is.EqualTo("none"));
    }

    [Test]
    public void From_AuthType_IsCaseInsensitive()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]  = "https://ado.company.com/Col/Proj",
            [ConfigKeys.AuthType] = "NTLM"
        });

        Assert.That(config.AuthType, Is.EqualTo("ntlm"));
    }

    // ── NTLM / Negotiate credentials ────────────────────────────────────────

    [Test]
    public void From_NtlmWithCredentials_PopulatesUsernamePasswordDomain()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]  = "https://ado.company.com/Col/Proj",
            [ConfigKeys.AuthType] = "ntlm",
            [ConfigKeys.Username] = "svc_account",
            [ConfigKeys.Password] = "secret",
            [ConfigKeys.Domain]   = "CORP"
        });

        Assert.That(config.Username, Is.EqualTo("svc_account"));
        Assert.That(config.Password, Is.EqualTo("secret"));
        Assert.That(config.Domain,   Is.EqualTo("CORP"));
    }

    [Test]
    public void From_NtlmWithoutCredentials_NullUsernameIndicatesProcessIdentity()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]  = "https://ado.company.com/Col/Proj",
            [ConfigKeys.AuthType] = "ntlm"
        });

        Assert.That(config.Username, Is.Null);
    }

    // ── API key credentials ──────────────────────────────────────────────────

    [Test]
    public void From_ApiKey_PopulatesHeaderAndValue()
    {
        var config = MakeConfig(new()
        {
            [ConfigKeys.BaseUrl]      = "https://ado.company.com/Col/Proj",
            [ConfigKeys.AuthType]     = "apikey",
            [ConfigKeys.ApiKeyHeader] = "X-Api-Key",
            [ConfigKeys.ApiKeyValue]  = "supersecret"
        });

        Assert.That(config.ApiKeyHeader, Is.EqualTo("X-Api-Key"));
        Assert.That(config.ApiKeyValue,  Is.EqualTo("supersecret"));
    }

    // ── Helpers ──────────────────────────────────────────────────────────────

    private static AdoConnectionConfig MakeConfig(Dictionary<string, string> raw)
        => AdoConnectionConfig.From(raw);
}
