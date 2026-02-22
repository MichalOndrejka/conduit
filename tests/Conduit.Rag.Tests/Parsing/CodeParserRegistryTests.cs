using Conduit.Rag.Parsing;
using Moq;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class CodeParserRegistryTests
{
    [Test]
    public void Resolve_MatchingParser_ReturnsIt()
    {
        var parser = new Mock<ICodeParser>();
        parser.Setup(p => p.CanParse(".cs")).Returns(true);

        var registry = new CodeParserRegistry([parser.Object]);

        Assert.That(registry.Resolve(".cs"), Is.SameAs(parser.Object));
    }

    [Test]
    public void Resolve_NoMatchingParser_ReturnsNull()
    {
        var parser = new Mock<ICodeParser>();
        parser.Setup(p => p.CanParse(It.IsAny<string>())).Returns(false);

        var registry = new CodeParserRegistry([parser.Object]);

        Assert.That(registry.Resolve(".xyz"), Is.Null);
    }

    [Test]
    public void Resolve_EmptyRegistry_ReturnsNull()
    {
        var registry = new CodeParserRegistry([]);

        Assert.That(registry.Resolve(".cs"), Is.Null);
    }

    [Test]
    public void Resolve_MultipleMatching_ReturnsFirst()
    {
        var first  = new Mock<ICodeParser>();
        var second = new Mock<ICodeParser>();
        first.Setup(p => p.CanParse(".ts")).Returns(true);
        second.Setup(p => p.CanParse(".ts")).Returns(true);

        var registry = new CodeParserRegistry([first.Object, second.Object]);

        Assert.That(registry.Resolve(".ts"), Is.SameAs(first.Object));
    }

    [Test]
    public void Resolve_CaseVariant_DelegatedToParser()
    {
        var parser = new Mock<ICodeParser>();
        parser.Setup(p => p.CanParse(".CS")).Returns(true);
        parser.Setup(p => p.CanParse(".cs")).Returns(false);

        var registry = new CodeParserRegistry([parser.Object]);

        // Registry itself doesn't normalise case — that is the parser's responsibility
        Assert.That(registry.Resolve(".CS"), Is.SameAs(parser.Object));
        Assert.That(registry.Resolve(".cs"), Is.Null);
    }
}
