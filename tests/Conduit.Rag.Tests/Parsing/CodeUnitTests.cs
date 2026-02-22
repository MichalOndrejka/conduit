using Conduit.Rag.Parsing;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class CodeUnitTests
{
    // ── ToIdSlug — stability ──────────────────────────────────────

    [Test]
    public void ToIdSlug_CalledTwiceWithSameInput_ReturnsSameSlug()
    {
        var unit = new CodeUnit { Name = "GetUserById", Signature = "GetUserById(Guid id) -> User?" };

        Assert.That(unit.ToIdSlug(), Is.EqualTo(unit.ToIdSlug()));
    }

    [Test]
    public void ToIdSlug_StableAcrossNewInstances_ReturnsSameSlug()
    {
        var unit1 = new CodeUnit { Name = "Process", Signature = "Process(string input) -> bool" };
        var unit2 = new CodeUnit { Name = "Process", Signature = "Process(string input) -> bool" };

        Assert.That(unit1.ToIdSlug(), Is.EqualTo(unit2.ToIdSlug()));
    }

    [Test]
    public void ToIdSlug_DifferentSignaturesSameName_ReturnsDifferentSlugs()
    {
        var unit1 = new CodeUnit { Name = "Get", Signature = "Get(int id) -> User" };
        var unit2 = new CodeUnit { Name = "Get", Signature = "Get(string name) -> User" };

        Assert.That(unit1.ToIdSlug(), Is.Not.EqualTo(unit2.ToIdSlug()));
    }

    [Test]
    public void ToIdSlug_WithContainerName_IncludesBothParts()
    {
        var unit = new CodeUnit { Name = "Execute", ContainerName = "CommandHandler" };

        var slug = unit.ToIdSlug();

        Assert.That(slug, Does.Contain("commandhandler").And.Contain("execute"));
    }

    [Test]
    public void ToIdSlug_WithoutContainerName_UsesNameOnly()
    {
        var unit = new CodeUnit { Name = "standalone" };

        Assert.That(unit.ToIdSlug(), Is.EqualTo("standalone"));
    }

    [Test]
    public void ToIdSlug_SpecialCharacters_ReplacedWithHyphens()
    {
        var unit = new CodeUnit { Name = "My Type<T>" };

        var slug = unit.ToIdSlug();

        Assert.That(slug, Does.Match(@"^[a-z0-9\-]+$"));
    }

    [Test]
    public void ToIdSlug_NoSignature_NoHashSuffix()
    {
        var unit = new CodeUnit { Name = "MyClass" };

        // Without a signature there should be no "-NNNNN" suffix
        Assert.That(unit.ToIdSlug(), Is.EqualTo("myclass"));
    }

    // ── BuildEnrichedText ─────────────────────────────────────────

    [Test]
    public void BuildEnrichedText_ContainsLanguageHeader()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "Foo.cs" };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("// Language: C#"));
    }

    [Test]
    public void BuildEnrichedText_ContainsFilePathHeader()
    {
        var unit = new CodeUnit { Language = "Go", FilePath = "pkg/service.go" };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("// File: pkg/service.go"));
    }

    [Test]
    public void BuildEnrichedText_WhenNamespacePresent_ContainsNamespaceHeader()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", Namespace = "MyApp.Services" };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("// Namespace: MyApp.Services"));
    }

    [Test]
    public void BuildEnrichedText_WhenNoNamespace_OmitsNamespaceLine()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", Namespace = null };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Not.Contain("// Namespace:"));
    }

    [Test]
    public void BuildEnrichedText_WhenContainerPresent_ContainsTypeHeader()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", ContainerName = "UserService" };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("// Type: UserService"));
    }

    [Test]
    public void BuildEnrichedText_WhenSignaturePresent_ContainsMemberHeader()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", Signature = "GetById(int id) -> User" };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("// Member: GetById(int id) -> User"));
    }

    [Test]
    public void BuildEnrichedText_WhenNoSignature_ContainsNameHeader()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", Name = "UserService", Signature = null };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("// Name: UserService"));
    }

    [Test]
    public void BuildEnrichedText_ContainsVisibilityHeader()
    {
        var pub  = new CodeUnit { Language = "C#", FilePath = "f.cs", IsPublic = true };
        var priv = new CodeUnit { Language = "C#", FilePath = "f.cs", IsPublic = false };

        Assert.That(CodeUnit.BuildEnrichedText(pub),  Does.Contain("// Visibility: public"));
        Assert.That(CodeUnit.BuildEnrichedText(priv), Does.Contain("// Visibility: internal"));
    }

    [Test]
    public void BuildEnrichedText_ContainsKindHeader()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", Kind = CodeUnitKind.Method };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("// Kind: method"));
    }

    [Test]
    public void BuildEnrichedText_WhenDocCommentPresent_IncludedBeforeFullText()
    {
        var unit = new CodeUnit
        {
            Language   = "C#",
            FilePath   = "f.cs",
            DocComment = "Gets user by ID.",
            FullText   = "public User GetById(int id) { }",
        };

        var text = CodeUnit.BuildEnrichedText(unit);
        var docPos  = text.IndexOf("Gets user by ID.", StringComparison.Ordinal);
        var codePos = text.IndexOf("public User", StringComparison.Ordinal);

        Assert.That(docPos, Is.GreaterThan(0));
        Assert.That(docPos, Is.LessThan(codePos));
    }

    [Test]
    public void BuildEnrichedText_ContainsFullText()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", FullText = "public void Foo() {}" };

        Assert.That(CodeUnit.BuildEnrichedText(unit), Does.Contain("public void Foo() {}"));
    }

    [Test]
    public void EnrichedText_Property_MatchesBuildEnrichedText()
    {
        var unit = new CodeUnit { Language = "C#", FilePath = "f.cs", Name = "Foo", Kind = CodeUnitKind.Class };

        Assert.That(unit.EnrichedText, Is.EqualTo(CodeUnit.BuildEnrichedText(unit)));
    }
}
