using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class CSharpParserTests
{
    private readonly CSharpParser _parser = new();

    // ── CanParse ──────────────────────────────────────────────────

    [TestCase(".cs")]
    [TestCase(".CS")]
    [TestCase(".Cs")]
    public void CanParse_SupportedExtension_ReturnsTrue(string ext)
        => Assert.That(_parser.CanParse(ext), Is.True);

    [TestCase(".ts")]
    [TestCase(".js")]
    [TestCase(".go")]
    [TestCase("")]
    public void CanParse_UnsupportedExtension_ReturnsFalse(string ext)
        => Assert.That(_parser.CanParse(ext), Is.False);

    // ── Class ─────────────────────────────────────────────────────

    [Test]
    public void Parse_PublicClass_EmitsClassUnit()
    {
        var units = _parser.Parse("public class UserService {}", "UserService.cs");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Class && u.Name == "UserService"), Is.True);
    }

    [Test]
    public void Parse_PublicClass_IsPublicTrue()
    {
        var units = _parser.Parse("public class Foo {}", "Foo.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Class).IsPublic, Is.True);
    }

    [Test]
    public void Parse_InternalClass_IsPublicFalse()
    {
        var units = _parser.Parse("internal class Foo {}", "Foo.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Class).IsPublic, Is.False);
    }

    [Test]
    public void Parse_Class_SetsNamespace()
    {
        var code  = "namespace MyApp.Services { public class UserService {} }";
        var units = _parser.Parse(code, "UserService.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Class).Namespace, Is.EqualTo("MyApp.Services"));
    }

    [Test]
    public void Parse_Class_FullTextContainsDeclaration()
    {
        var units = _parser.Parse("public class Foo { }", "Foo.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Class).FullText, Does.Contain("class Foo"));
    }

    [Test]
    public void Parse_Class_LanguageIsCSharp()
    {
        var units = _parser.Parse("public class Foo {}", "Foo.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Class).Language, Is.EqualTo("C#"));
    }

    [Test]
    public void Parse_Class_FilePathIsSet()
    {
        var units = _parser.Parse("public class Foo {}", "src/Foo.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Class).FilePath, Is.EqualTo("src/Foo.cs"));
    }

    // ── Nested class ──────────────────────────────────────────────

    [Test]
    public void Parse_NestedClass_ContainerNameIsOuterClass()
    {
        var code  = "public class Outer { public class Inner {} }";
        var units = _parser.Parse(code, "Outer.cs");

        var inner = units.First(u => u.Name == "Inner");
        Assert.That(inner.ContainerName, Is.EqualTo("Outer"));
    }

    // ── Interface / Record / Struct ───────────────────────────────

    [Test]
    public void Parse_Interface_EmitsInterfaceUnit()
    {
        var units = _parser.Parse("public interface IFoo { void Bar(); }", "IFoo.cs");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Interface && u.Name == "IFoo"), Is.True);
    }

    [Test]
    public void Parse_Record_EmitsRecordUnit()
    {
        var units = _parser.Parse("public record Point(int X, int Y);", "Point.cs");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Record && u.Name == "Point"), Is.True);
    }

    [Test]
    public void Parse_Struct_EmitsStructUnit()
    {
        var units = _parser.Parse("public struct Vector2 { public float X, Y; }", "Vector2.cs");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Struct && u.Name == "Vector2"), Is.True);
    }

    // ── Enum ──────────────────────────────────────────────────────

    [Test]
    public void Parse_Enum_EmitsSingleUnit()
    {
        var units = _parser.Parse("public enum Color { Red, Green, Blue }", "Color.cs");

        Assert.That(units.Count(u => u.Kind == CodeUnitKind.Enum), Is.EqualTo(1));
    }

    [Test]
    public void Parse_Enum_DoesNotEmitMemberUnits()
    {
        var units = _parser.Parse("public enum Color { Red, Green, Blue }", "Color.cs");

        // Enum members should NOT be emitted as separate units
        Assert.That(units.Any(u => u.Name is "Red" or "Green" or "Blue"), Is.False);
    }

    // ── Method ────────────────────────────────────────────────────

    [Test]
    public void Parse_Method_EmitsMethodUnit()
    {
        var code  = "public class Foo { public int Add(int a, int b) => a + b; }";
        var units = _parser.Parse(code, "Foo.cs");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Method && u.Name == "Add"), Is.True);
    }

    [Test]
    public void Parse_Method_ContainerNameIsDeclaringType()
    {
        var code  = "public class Foo { public void Bar() {} }";
        var units = _parser.Parse(code, "Foo.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Method).ContainerName, Is.EqualTo("Foo"));
    }

    [Test]
    public void Parse_Method_SignatureContainsParameterTypes()
    {
        var code  = "public class Foo { public string Get(int id, string name) => \"\"; }";
        var units = _parser.Parse(code, "Foo.cs");

        var sig = units.First(u => u.Kind == CodeUnitKind.Method).Signature;
        Assert.That(sig, Does.Contain("int id").And.Contain("string name"));
    }

    [Test]
    public void Parse_Method_SignatureContainsReturnType()
    {
        var code  = "public class Foo { public User? GetById(int id) => null; }";
        var units = _parser.Parse(code, "Foo.cs");

        var sig = units.First(u => u.Kind == CodeUnitKind.Method).Signature;
        Assert.That(sig, Does.Contain("->").And.Contain("User?"));
    }

    // ── Constructor ───────────────────────────────────────────────

    [Test]
    public void Parse_Constructor_EmitsConstructorUnit()
    {
        var code  = "public class Foo { public Foo(string name) {} }";
        var units = _parser.Parse(code, "Foo.cs");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Constructor), Is.True);
    }

    // ── Property ──────────────────────────────────────────────────

    [Test]
    public void Parse_Property_EmitsPropertyUnit()
    {
        var code  = "public class Foo { public string Name { get; set; } }";
        var units = _parser.Parse(code, "Foo.cs");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Property && u.Name == "Name"), Is.True);
    }

    // ── XML doc comment ───────────────────────────────────────────

    [Test]
    public void Parse_XmlDocComment_ExtractedToDocComment()
    {
        var code = """
            /// <summary>Gets user by ID.</summary>
            public class UserService {}
            """;
        var units = _parser.Parse(code, "UserService.cs");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Class).DocComment, Is.Not.Null);
    }

    // ── Enriched text ─────────────────────────────────────────────

    [Test]
    public void Parse_Unit_EnrichedTextContainsLanguageAndFile()
    {
        var units = _parser.Parse("public class Foo {}", "src/Foo.cs");
        var unit  = units.First(u => u.Kind == CodeUnitKind.Class);

        Assert.That(unit.EnrichedText, Does.Contain("// Language: C#"));
        Assert.That(unit.EnrichedText, Does.Contain("// File: src/Foo.cs"));
    }

    // ── Error resilience ──────────────────────────────────────────

    [Test]
    public void Parse_EmptyContent_ReturnsEmptyList()
        => Assert.That(_parser.Parse("", "empty.cs"), Is.Empty);

    [Test]
    public void Parse_WhitespaceOnly_ReturnsEmptyList()
        => Assert.That(_parser.Parse("   \n  ", "blank.cs"), Is.Empty);

    [Test]
    public void Parse_InvalidSyntax_DoesNotThrow()
        => Assert.DoesNotThrow(() => _parser.Parse("this is NOT valid C# !!!{}{{", "bad.cs"));
}
