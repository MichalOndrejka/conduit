using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class PowerShellParserTests
{
    private readonly PowerShellParser _parser = new();

    // ── CanParse ──────────────────────────────────────────────────

    [TestCase(".ps1")]
    [TestCase(".psm1")]
    [TestCase(".PS1")]
    [TestCase(".PSM1")]
    public void CanParse_SupportedExtension_ReturnsTrue(string ext)
        => Assert.That(_parser.CanParse(ext), Is.True);

    [TestCase(".py")]
    [TestCase(".sh")]
    [TestCase(".cs")]
    [TestCase("")]
    public void CanParse_UnsupportedExtension_ReturnsFalse(string ext)
        => Assert.That(_parser.CanParse(ext), Is.False);

    // ── Function detection ────────────────────────────────────────

    [Test]
    public void Parse_Function_EmitsFunctionUnit()
    {
        var code  = "function Get-User {\n    param($Id)\n    return $Id\n}";
        var units = _parser.Parse(code, "users.ps1");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Function && u.Name == "Get-User"), Is.True);
    }

    [Test]
    public void Parse_FunctionKeywordCaseInsensitive_Detected()
    {
        // PowerShell is case-insensitive
        var code  = "Function Write-Log {\n    Write-Output 'log'\n}";
        var units = _parser.Parse(code, "log.ps1");

        Assert.That(units.Any(u => u.Name == "Write-Log"), Is.True);
    }

    [Test]
    public void Parse_MultipleFunctions_AllEmitted()
    {
        var code  = "function Foo {\n    'foo'\n}\nfunction Bar {\n    'bar'\n}";
        var units = _parser.Parse(code, "multi.ps1");

        Assert.That(units, Has.Count.EqualTo(2));
    }

    // ── Visibility ────────────────────────────────────────────────

    [Test]
    public void Parse_PublicFunction_IsPublicTrue()
    {
        var units = _parser.Parse("function Invoke-Action {\n}", "actions.ps1");

        Assert.That(units.First().IsPublic, Is.True);
    }

    [Test]
    public void Parse_UnderscorePrefixedFunction_IsPublicFalse()
    {
        var units = _parser.Parse("function _InternalHelper {\n}", "helpers.ps1");

        Assert.That(units.First().IsPublic, Is.False);
    }

    // ── SYNOPSIS extraction ───────────────────────────────────────

    [Test]
    public void Parse_FunctionWithSynopsis_ExtractsDocComment()
    {
        var code = """
            <#
            .SYNOPSIS
            Gets a user by their unique identifier.
            #>
            function Get-User {
                param($Id)
            }
            """;
        var units = _parser.Parse(code, "users.ps1");

        Assert.That(units.First().DocComment,
            Does.Contain("Gets a user by their unique identifier"));
    }

    [Test]
    public void Parse_FunctionWithoutSynopsis_DocCommentIsNull()
    {
        var code  = "function Plain-Func {\n    'plain'\n}";
        var units = _parser.Parse(code, "plain.ps1");

        Assert.That(units.First().DocComment, Is.Null);
    }

    [Test]
    public void Parse_SynopsisStopsAtNextSection_OnlyFirstSectionExtracted()
    {
        var code = """
            <#
            .SYNOPSIS
            Short synopsis here.
            .DESCRIPTION
            Longer description that should not be included.
            #>
            function Test-It {
            }
            """;
        var units = _parser.Parse(code, "test.ps1");
        var doc   = units.First().DocComment!;

        Assert.That(doc, Does.Contain("Short synopsis here"));
        Assert.That(doc, Does.Not.Contain("Longer description"));
    }

    // ── Full text ─────────────────────────────────────────────────

    [Test]
    public void Parse_Function_FullTextContainsFunctionBody()
    {
        var code  = "function Get-Greeting {\n    return 'Hello'\n}";
        var units = _parser.Parse(code, "greet.ps1");

        Assert.That(units.First().FullText, Does.Contain("Get-Greeting").And.Contain("Hello"));
    }

    // ── Error resilience ──────────────────────────────────────────

    [Test]
    public void Parse_EmptyContent_ReturnsEmptyList()
        => Assert.That(_parser.Parse("", "empty.ps1"), Is.Empty);

    [Test]
    public void Parse_NoFunctions_ReturnsEmptyList()
        => Assert.That(_parser.Parse("# Just a comment\n$x = 1", "script.ps1"), Is.Empty);

    [Test]
    public void Parse_DoesNotThrow_OnAnyInput()
        => Assert.DoesNotThrow(() => _parser.Parse("!!@@##$$", "broken.ps1"));
}
