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
        Environment.SetEnvironmentVariable("CONDUIT_TEST_TOKEN", "my-token");
        try
        {
            var config = MakeConfig(new()
            {
                [ConfigKeys.BaseUrl]  = "https://ado.company.com/Col/Proj",
                [ConfigKeys.AuthType] = "bearer",
                [ConfigKeys.Token]    = "CONDUIT_TEST_TOKEN"
            });

            Assert.That(config.AuthType, Is.EqualTo("bearer"));
            Assert.That(config.Token,    Is.EqualTo("my-token"));
        }
        finally { Environment.SetEnvironmentVariable("CONDUIT_TEST_TOKEN", null); }
    }

    [Test]
    public void From_WithPatKeyAndNoAuthType_DefaultsToPat()
    {
        Environment.SetEnvironmentVariable("CONDUIT_TEST_PAT", "my-pat");
        try
        {
            var config = MakeConfig(new()
            {
                [ConfigKeys.Organization] = "org",
                [ConfigKeys.Project]      = "proj",
                [ConfigKeys.Pat]          = "CONDUIT_TEST_PAT"
            });

            Assert.That(config.AuthType, Is.EqualTo("pat"));
            Assert.That(config.Pat,      Is.EqualTo("my-pat"));
        }
        finally { Environment.SetEnvironmentVariable("CONDUIT_TEST_PAT", null); }
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
        Environment.SetEnvironmentVariable("CONDUIT_TEST_PASS", "secret");
        try
        {
            var config = MakeConfig(new()
            {
                [ConfigKeys.BaseUrl]  = "https://ado.company.com/Col/Proj",
                [ConfigKeys.AuthType] = "ntlm",
                [ConfigKeys.Username] = "svc_account",
                [ConfigKeys.Password] = "CONDUIT_TEST_PASS",
                [ConfigKeys.Domain]   = "CORP"
            });

            Assert.That(config.Username, Is.EqualTo("svc_account"));
            Assert.That(config.Password, Is.EqualTo("secret"));
            Assert.That(config.Domain,   Is.EqualTo("CORP"));
        }
        finally { Environment.SetEnvironmentVariable("CONDUIT_TEST_PASS", null); }
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
        Environment.SetEnvironmentVariable("CONDUIT_TEST_APIKEY", "supersecret");
        try
        {
            var config = MakeConfig(new()
            {
                [ConfigKeys.BaseUrl]      = "https://ado.company.com/Col/Proj",
                [ConfigKeys.AuthType]     = "apikey",
                [ConfigKeys.ApiKeyHeader] = "X-Api-Key",
                [ConfigKeys.ApiKeyValue]  = "CONDUIT_TEST_APIKEY"
            });

            Assert.That(config.ApiKeyHeader, Is.EqualTo("X-Api-Key"));
            Assert.That(config.ApiKeyValue,  Is.EqualTo("supersecret"));
        }
        finally { Environment.SetEnvironmentVariable("CONDUIT_TEST_APIKEY", null); }
    }

    // ── Helpers ──────────────────────────────────────────────────────────────

    private static AdoConnectionConfig MakeConfig(Dictionary<string, string> raw)
        => AdoConnectionConfig.From(raw);
}
