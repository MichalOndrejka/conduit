using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class GoParserTests
{
    private readonly GoParser _parser = new();

    // ── CanParse ──────────────────────────────────────────────────

    [TestCase(".go")]
    [TestCase(".GO")]
    public void CanParse_GoExtension_ReturnsTrue(string ext)
        => Assert.That(_parser.CanParse(ext), Is.True);

    [TestCase(".cs")]
    [TestCase(".ts")]
    [TestCase("")]
    public void CanParse_OtherExtension_ReturnsFalse(string ext)
        => Assert.That(_parser.CanParse(ext), Is.False);

    // ── Function ──────────────────────────────────────────────────

    [Test]
    public void Parse_Function_EmitsFunctionUnit()
    {
        var code  = "package main\nfunc Hello() string {\n\treturn \"Hello\"\n}";
        var units = _parser.Parse(code, "main.go");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Function && u.Name == "Hello"), Is.True);
    }

    [Test]
    public void Parse_ExportedFunction_IsPublicTrue()
    {
        var units = _parser.Parse("package main\nfunc Exported() {}", "main.go");

        Assert.That(units.First(u => u.Name == "Exported").IsPublic, Is.True);
    }

    [Test]
    public void Parse_UnexportedFunction_IsPublicFalse()
    {
        var units = _parser.Parse("package main\nfunc unexported() {}", "main.go");

        Assert.That(units.First(u => u.Name == "unexported").IsPublic, Is.False);
    }

    [Test]
    public void Parse_Function_SignatureIsDeclarationLine()
    {
        var code  = "package main\nfunc Add(a, b int) int {\n\treturn a + b\n}";
        var units = _parser.Parse(code, "math.go");

        Assert.That(units.First(u => u.Name == "Add").Signature, Does.Contain("func Add(a, b int) int"));
    }

    [Test]
    public void Parse_MethodWithPointerReceiver_ContainerNameSet()
    {
        var code  = "package main\nfunc (s *UserService) GetById(id int) string {\n\treturn \"\"\n}";
        var units = _parser.Parse(code, "service.go");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Function).ContainerName, Is.EqualTo("UserService"));
    }

    [Test]
    public void Parse_MethodWithValueReceiver_ContainerNameSet()
    {
        var code  = "package main\nfunc (p Point) String() string {\n\treturn \"\"\n}";
        var units = _parser.Parse(code, "point.go");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Function).ContainerName, Is.EqualTo("Point"));
    }

    [Test]
    public void Parse_Function_PackageSetAsNamespace()
    {
        var code  = "package mypackage\nfunc Foo() {}";
        var units = _parser.Parse(code, "foo.go");

        Assert.That(units.First().Namespace, Is.EqualTo("mypackage"));
    }

    // ── Go doc comment ────────────────────────────────────────────

    [Test]
    public void Parse_GoDocAboveFunction_ExtractedToDocComment()
    {
        var code = "package main\n// Greet says hello to the caller.\nfunc Greet() string {\n\treturn \"\"\n}";
        var units = _parser.Parse(code, "main.go");

        Assert.That(units.First(u => u.Name == "Greet").DocComment,
            Does.Contain("Greet says hello to the caller"));
    }

    [Test]
    public void Parse_MultilineGoDoc_AllLinesExtracted()
    {
        var code = "package main\n// Line one.\n// Line two.\nfunc Documented() {}";
        var units = _parser.Parse(code, "doc.go");

        var doc = units.First(u => u.Name == "Documented").DocComment;
        Assert.That(doc, Does.Contain("Line one").And.Contain("Line two"));
    }

    // ── Struct ────────────────────────────────────────────────────

    [Test]
    public void Parse_Struct_EmitsStructUnit()
    {
        var code  = "package main\ntype Person struct {\n\tName string\n\tAge  int\n}";
        var units = _parser.Parse(code, "person.go");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Struct && u.Name == "Person"), Is.True);
    }

    [Test]
    public void Parse_ExportedStruct_IsPublicTrue()
    {
        var units = _parser.Parse("package main\ntype MyStruct struct {}", "s.go");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Struct).IsPublic, Is.True);
    }

    [Test]
    public void Parse_UnexportedStruct_IsPublicFalse()
    {
        var units = _parser.Parse("package main\ntype myStruct struct {}", "s.go");

        Assert.That(units.First(u => u.Kind == CodeUnitKind.Struct).IsPublic, Is.False);
    }

    // ── Interface ─────────────────────────────────────────────────

    [Test]
    public void Parse_Interface_EmitsInterfaceUnit()
    {
        var code  = "package main\ntype Reader interface {\n\tRead() string\n}";
        var units = _parser.Parse(code, "reader.go");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Interface && u.Name == "Reader"), Is.True);
    }

    // ── Multiple declarations ─────────────────────────────────────

    [Test]
    public void Parse_MixedDeclarations_AllEmitted()
    {
        var code = """
            package main

            type MyStruct struct {
                Field string
            }

            type MyInterface interface {
                Do() string
            }

            func MyFunc() string {
                return ""
            }
            """;
        var units = _parser.Parse(code, "mixed.go");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Struct),    Is.True);
        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Interface), Is.True);
        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Function),  Is.True);
    }

    // ── Error resilience ──────────────────────────────────────────

    [Test]
    public void Parse_EmptyContent_ReturnsEmptyList()
        => Assert.That(_parser.Parse("", "empty.go"), Is.Empty);

    [Test]
    public void Parse_NoDeclarations_ReturnsEmptyList()
        => Assert.That(_parser.Parse("package main\n// just a comment", "noop.go"), Is.Empty);

    [Test]
    public void Parse_DoesNotThrow_OnAnyInput()
        => Assert.DoesNotThrow(() => _parser.Parse("random garbage !!!{{{", "broken.go"));
}
