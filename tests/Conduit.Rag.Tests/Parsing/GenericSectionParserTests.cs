using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class GenericSectionParserTests
{
    private readonly GenericSectionParser _parser = new();

    // ── CanParse ──────────────────────────────────────────────────

    [TestCase(".yaml")]
    [TestCase(".yml")]
    [TestCase(".json")]
    [TestCase(".YAML")]
    [TestCase(".JSON")]
    [TestCase("dockerfile")]   // extension-less Dockerfile passed as filename key
    public void CanParse_SupportedExtension_ReturnsTrue(string ext)
        => Assert.That(_parser.CanParse(ext), Is.True);

    [TestCase(".cs")]
    [TestCase(".go")]
    [TestCase(".md")]
    [TestCase("")]
    public void CanParse_UnsupportedExtension_ReturnsFalse(string ext)
        => Assert.That(_parser.CanParse(ext), Is.False);

    // ── Always one unit ───────────────────────────────────────────

    [Test]
    public void Parse_YamlFile_ReturnsExactlyOneUnit()
    {
        var units = _parser.Parse("key: value\nother: 123", "config.yaml");

        Assert.That(units, Has.Count.EqualTo(1));
    }

    [Test]
    public void Parse_JsonFile_ReturnsExactlyOneUnit()
    {
        var units = _parser.Parse("{\"key\": \"value\"}", "settings.json");

        Assert.That(units, Has.Count.EqualTo(1));
    }

    [Test]
    public void Parse_EmptyContent_ReturnsOneUnit()
    {
        var units = _parser.Parse("", "empty.yaml");

        Assert.That(units, Has.Count.EqualTo(1));
    }

    // ── Unit properties ───────────────────────────────────────────

    [Test]
    public void Parse_Unit_KindIsFile()
    {
        var units = _parser.Parse("key: value", "config.yml");

        Assert.That(units[0].Kind, Is.EqualTo(CodeUnitKind.File));
    }

    [Test]
    public void Parse_Unit_NameIsFileName()
    {
        var units = _parser.Parse("version: '3'", "docker-compose.yaml");

        Assert.That(units[0].Name, Is.EqualTo("docker-compose.yaml"));
    }

    [Test]
    public void Parse_Unit_FullTextIsContentTrimmed()
    {
        var units = _parser.Parse("  key: value  ", "config.yml");

        Assert.That(units[0].FullText, Is.EqualTo("key: value"));
    }

    [Test]
    public void Parse_Unit_IsPublicTrue()
    {
        var units = _parser.Parse("{}", "data.json");

        Assert.That(units[0].IsPublic, Is.True);
    }

    [Test]
    public void Parse_YamlUnit_LanguageIsYAML()
    {
        var units = _parser.Parse("key: value", "config.yaml");

        Assert.That(units[0].Language, Is.EqualTo("YAML"));
    }

    [Test]
    public void Parse_JsonUnit_LanguageIsJSON()
    {
        var units = _parser.Parse("{}", "settings.json");

        Assert.That(units[0].Language, Is.EqualTo("JSON"));
    }

    [Test]
    public void Parse_Unit_FilePathIsSet()
    {
        var units = _parser.Parse("key: value", "deploy/config.yaml");

        Assert.That(units[0].FilePath, Is.EqualTo("deploy/config.yaml"));
    }

    // ── Error resilience ──────────────────────────────────────────

    [Test]
    public void Parse_DoesNotThrow_OnAnyInput()
        => Assert.DoesNotThrow(() => _parser.Parse("!!! @@@ ###", "weird.yaml"));
}
