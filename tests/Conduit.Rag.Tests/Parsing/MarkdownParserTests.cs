using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class MarkdownParserTests
{
    private readonly MarkdownParser _parser = new();

    // ── CanParse ──────────────────────────────────────────────────

    [TestCase(".md")]
    [TestCase(".MD")]
    [TestCase(".mdx")]
    [TestCase(".MDX")]
    public void CanParse_SupportedExtension_ReturnsTrue(string ext)
        => Assert.That(_parser.CanParse(ext), Is.True);

    [TestCase(".txt")]
    [TestCase(".rst")]
    [TestCase(".cs")]
    [TestCase("")]
    public void CanParse_UnsupportedExtension_ReturnsFalse(string ext)
        => Assert.That(_parser.CanParse(ext), Is.False);

    // ── Heading-based splitting ───────────────────────────────────

    [Test]
    public void Parse_ThreeHeadings_ReturnsThreeUnits()
    {
        var content = "# Intro\nText\n## Details\nMore\n### Sub\nSub text";
        var units   = _parser.Parse(content, "README.md");

        Assert.That(units, Has.Count.EqualTo(3));
    }

    [Test]
    public void Parse_HeadingNames_AreExtractedCorrectly()
    {
        var content = "# Getting Started\nContent\n## Configuration\nConfig";
        var units   = _parser.Parse(content, "doc.md");

        Assert.That(units[0].Name, Is.EqualTo("Getting Started"));
        Assert.That(units[1].Name, Is.EqualTo("Configuration"));
    }

    [Test]
    public void Parse_SectionText_ContainsHeadingAndBodyContent()
    {
        var content = "# Hello\nWorld";
        var units   = _parser.Parse(content, "doc.md");

        Assert.That(units[0].FullText, Does.Contain("# Hello").And.Contain("World"));
    }

    [Test]
    public void Parse_SectionBoundary_ContentNotLeakedToNextSection()
    {
        var content = "# Section A\nA content\n# Section B\nB content";
        var units   = _parser.Parse(content, "doc.md");

        Assert.That(units[0].FullText, Does.Not.Contain("B content"));
        Assert.That(units[1].FullText, Does.Not.Contain("A content"));
    }

    [Test]
    public void Parse_AllHeadingLevels_Recognised()
    {
        var content = "# H1\n## H2\n### H3\n#### H4\n##### H5\n###### H6";
        var units   = _parser.Parse(content, "levels.md");

        Assert.That(units, Has.Count.EqualTo(6));
    }

    // ── No headings fallback ──────────────────────────────────────

    [Test]
    public void Parse_NoHeadings_ReturnsSingleWholeFileUnit()
    {
        var content = "Just some prose without any headings.";
        var units   = _parser.Parse(content, "notes.md");

        Assert.That(units, Has.Count.EqualTo(1));
        Assert.That(units[0].FullText, Does.Contain("Just some prose"));
    }

    [Test]
    public void Parse_NoHeadings_NameIsFileNameWithoutExtension()
    {
        var units = _parser.Parse("Some text", "architecture-notes.md");

        Assert.That(units[0].Name, Is.EqualTo("architecture-notes"));
    }

    // ── Metadata ──────────────────────────────────────────────────

    [Test]
    public void Parse_AllUnits_KindIsSection()
    {
        var content = "# A\nContent\n## B\nMore";
        var units   = _parser.Parse(content, "doc.md");

        Assert.That(units.All(u => u.Kind == CodeUnitKind.Section), Is.True);
    }

    [Test]
    public void Parse_AllUnits_LanguageIsMarkdown()
    {
        var content = "# Section\nContent";
        var units   = _parser.Parse(content, "doc.md");

        Assert.That(units.All(u => u.Language == "Markdown"), Is.True);
    }

    [Test]
    public void Parse_AllUnits_IsPublicTrue()
    {
        var content = "# Section\nContent";
        var units   = _parser.Parse(content, "doc.md");

        Assert.That(units.All(u => u.IsPublic), Is.True);
    }

    // ── Error resilience ──────────────────────────────────────────

    [Test]
    public void Parse_EmptyContent_ReturnsSingleUnit()
    {
        var units = _parser.Parse("", "empty.md");

        // Whole-file fallback — one unit even for empty files
        Assert.That(units, Has.Count.EqualTo(1));
    }

    [Test]
    public void Parse_DoesNotThrow_OnAnyInput()
        => Assert.DoesNotThrow(() => _parser.Parse("!!! @@@ ###", "weird.md"));
}
