using Conduit.Rag.Parsing;
using Conduit.Rag.Parsing.Languages;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Parsing;

[TestFixture]
public class TypeScriptParserTests
{
    private readonly TypeScriptParser _parser = new();

    // ── CanParse ──────────────────────────────────────────────────

    [TestCase(".ts")]
    [TestCase(".tsx")]
    [TestCase(".js")]
    [TestCase(".jsx")]
    [TestCase(".TS")]
    public void CanParse_SupportedExtension_ReturnsTrue(string ext)
        => Assert.That(_parser.CanParse(ext), Is.True);

    [TestCase(".cs")]
    [TestCase(".go")]
    [TestCase(".py")]
    [TestCase("")]
    public void CanParse_UnsupportedExtension_ReturnsFalse(string ext)
        => Assert.That(_parser.CanParse(ext), Is.False);

    // ── Exported function ─────────────────────────────────────────

    [Test]
    public void Parse_ExportedFunction_EmitsFunctionUnit()
    {
        var code  = "export function greet(name: string): string {\n  return `Hello ${name}`;\n}";
        var units = _parser.Parse(code, "greet.ts");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Function && u.Name == "greet"), Is.True);
    }

    [Test]
    public void Parse_ExportedFunction_IsPublicTrue()
    {
        var units = _parser.Parse("export function myFunc() {}", "mod.ts");

        Assert.That(units.First().IsPublic, Is.True);
    }

    // ── Exported class ────────────────────────────────────────────

    [Test]
    public void Parse_ExportedClass_EmitsClassUnit()
    {
        var code  = "export class UserService {\n  getUser(id: number) { return null; }\n}";
        var units = _parser.Parse(code, "user.service.ts");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Class && u.Name == "UserService"), Is.True);
    }

    // ── Exported interface ────────────────────────────────────────

    [Test]
    public void Parse_ExportedInterface_EmitsInterfaceUnit()
    {
        var code  = "export interface IUser {\n  id: number;\n  name: string;\n}";
        var units = _parser.Parse(code, "user.ts");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Interface && u.Name == "IUser"), Is.True);
    }

    // ── Exported type alias ───────────────────────────────────────

    [Test]
    public void Parse_ExportedTypeAlias_EmitsTypeUnit()
    {
        var code  = "export type UserId = string | number;";
        var units = _parser.Parse(code, "types.ts");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Type && u.Name == "UserId"), Is.True);
    }

    // ── Exported enum ─────────────────────────────────────────────

    [Test]
    public void Parse_ExportedEnum_EmitsEnumUnit()
    {
        var code  = "export enum Direction {\n  Up,\n  Down,\n}";
        var units = _parser.Parse(code, "direction.ts");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Enum && u.Name == "Direction"), Is.True);
    }

    // ── Arrow function export ─────────────────────────────────────

    [Test]
    public void Parse_ExportedArrowFunction_EmitsFunctionUnit()
    {
        var code  = "export const fetchUser = async (id: number) => {\n  return null;\n};";
        var units = _parser.Parse(code, "api.ts");

        Assert.That(units.Any(u => u.Kind == CodeUnitKind.Function && u.Name == "fetchUser"), Is.True);
    }

    // ── Internal declarations ─────────────────────────────────────

    [Test]
    public void Parse_InternalFunction_IsPublicFalse()
    {
        var units = _parser.Parse("function internalHelper() {}", "mod.ts");

        Assert.That(units.First().IsPublic, Is.False);
    }

    [Test]
    public void Parse_InternalClass_IsPublicFalse()
    {
        var units = _parser.Parse("class LocalHelper {\n  run() {}\n}", "mod.ts");

        Assert.That(units.First().IsPublic, Is.False);
    }

    // ── Multiple declarations ─────────────────────────────────────

    [Test]
    public void Parse_MultipleDeclarations_AllEmitted()
    {
        var code = """
            export class Foo {}
            export interface IBar {}
            export type Baz = string;
            """;
        var units = _parser.Parse(code, "multi.ts");

        Assert.That(units, Has.Count.EqualTo(3));
    }

    [Test]
    public void Parse_SameLineNotParsedTwice_NoDuplicates()
    {
        // export class matches both ExportedClass and a hypothetical fallback
        var code  = "export class MyClass {\n  doSomething() {}\n}";
        var units = _parser.Parse(code, "my.ts");

        Assert.That(units.Count(u => u.Name == "MyClass"), Is.EqualTo(1));
    }

    // ── JSDoc ─────────────────────────────────────────────────────

    [Test]
    public void Parse_JsDocAboveFunction_ExtractedToDocComment()
    {
        var code = """
            /**
             * Fetches a user by ID.
             */
            export function getUser(id: number) {
              return null;
            }
            """;
        var units = _parser.Parse(code, "api.ts");

        Assert.That(units.First(u => u.Name == "getUser").DocComment,
            Does.Contain("Fetches a user by ID"));
    }

    // ── Language tag ─────────────────────────────────────────────

    [Test]
    public void Parse_AllUnits_LanguageIsTypeScript()
    {
        var code  = "export function foo() {}\nexport class Bar {}";
        var units = _parser.Parse(code, "foo.ts");

        Assert.That(units.All(u => u.Language == "TypeScript"), Is.True);
    }

    // ── Error resilience ──────────────────────────────────────────

    [Test]
    public void Parse_EmptyContent_ReturnsEmptyList()
        => Assert.That(_parser.Parse("", "empty.ts"), Is.Empty);

    [Test]
    public void Parse_NoMatchingDeclarations_ReturnsEmptyList()
        => Assert.That(_parser.Parse("const x = 1;\nlet y = 2;", "vars.ts"), Is.Empty);

    [Test]
    public void Parse_DoesNotThrow_OnAnyInput()
        => Assert.DoesNotThrow(() => _parser.Parse("!!@@##$$%%", "broken.ts"));
}
