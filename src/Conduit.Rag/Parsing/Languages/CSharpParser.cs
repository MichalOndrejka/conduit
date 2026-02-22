using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.CSharp.Syntax;

namespace Conduit.Rag.Parsing.Languages;

/// <summary>
/// Parses C# source files using Roslyn into one <see cref="CodeUnit"/> per
/// class, interface, record, struct, enum, method, constructor, or property.
/// </summary>
public sealed class CSharpParser : ICodeParser
{
    public bool CanParse(string extension)
        => extension.Equals(".cs", StringComparison.OrdinalIgnoreCase);

    public IReadOnlyList<CodeUnit> Parse(string content, string filePath)
    {
        try
        {
            var tree   = CSharpSyntaxTree.ParseText(content);
            var root   = tree.GetCompilationUnitRoot();
            var walker = new Walker(filePath);
            walker.Visit(root);
            return walker.Units;
        }
        catch
        {
            return [];
        }
    }

    // ── Roslyn walker ────────────────────────────────────────────────────────

    private sealed class Walker(string filePath) : CSharpSyntaxWalker
    {
        private readonly Stack<string> _typeStack = new();
        public  readonly List<CodeUnit> Units     = [];

        // ── Type declarations ────────────────────────────────────────────────

        public override void VisitClassDeclaration(ClassDeclarationSyntax node)
        {
            EmitType(node, node.Identifier.Text, CodeUnitKind.Class);
            _typeStack.Push(node.Identifier.Text);
            base.VisitClassDeclaration(node);
            _typeStack.Pop();
        }

        public override void VisitInterfaceDeclaration(InterfaceDeclarationSyntax node)
        {
            EmitType(node, node.Identifier.Text, CodeUnitKind.Interface);
            _typeStack.Push(node.Identifier.Text);
            base.VisitInterfaceDeclaration(node);
            _typeStack.Pop();
        }

        public override void VisitRecordDeclaration(RecordDeclarationSyntax node)
        {
            EmitType(node, node.Identifier.Text, CodeUnitKind.Record);
            _typeStack.Push(node.Identifier.Text);
            base.VisitRecordDeclaration(node);
            _typeStack.Pop();
        }

        public override void VisitStructDeclaration(StructDeclarationSyntax node)
        {
            EmitType(node, node.Identifier.Text, CodeUnitKind.Struct);
            _typeStack.Push(node.Identifier.Text);
            base.VisitStructDeclaration(node);
            _typeStack.Pop();
        }

        public override void VisitEnumDeclaration(EnumDeclarationSyntax node)
        {
            // Emit enum as a single unit; do NOT recurse into members.
            EmitType(node, node.Identifier.Text, CodeUnitKind.Enum);
        }

        // ── Member declarations ──────────────────────────────────────────────

        public override void VisitMethodDeclaration(MethodDeclarationSyntax node)
        {
            EmitMember(node, node.Identifier.Text, CodeUnitKind.Method,
                BuildMethodSignature(node.Identifier.Text, node.ParameterList, node.ReturnType));
            base.VisitMethodDeclaration(node);
        }

        public override void VisitConstructorDeclaration(ConstructorDeclarationSyntax node)
        {
            EmitMember(node, node.Identifier.Text, CodeUnitKind.Constructor,
                BuildConstructorSignature(node.Identifier.Text, node.ParameterList));
            base.VisitConstructorDeclaration(node);
        }

        public override void VisitPropertyDeclaration(PropertyDeclarationSyntax node)
        {
            EmitMember(node, node.Identifier.Text, CodeUnitKind.Property,
                $"{node.Identifier.Text}: {node.Type}");
            base.VisitPropertyDeclaration(node);
        }

        // ── Helpers ──────────────────────────────────────────────────────────

        private void EmitType(MemberDeclarationSyntax node, string name, CodeUnitKind kind)
        {
            var containerName = _typeStack.Count > 0 ? _typeStack.Peek() : null;
            var ns            = GetNamespace(node);
            var isPublic      = HasPublicModifier(node);
            var doc           = ExtractDocComment(node);

            Units.Add(new CodeUnit
            {
                Kind          = kind,
                Name          = name,
                ContainerName = containerName,
                Namespace     = ns,
                Signature     = null,
                IsPublic      = isPublic,
                DocComment    = doc,
                FullText      = node.ToFullString().Trim(),
                Language      = "C#",
                FilePath      = filePath,
            });
        }

        private void EmitMember(MemberDeclarationSyntax node, string name, CodeUnitKind kind, string signature)
        {
            var containerName = _typeStack.Count > 0 ? _typeStack.Peek() : null;
            var ns            = GetNamespace(node);
            var isPublic      = HasPublicModifier(node);
            var doc           = ExtractDocComment(node);

            Units.Add(new CodeUnit
            {
                Kind          = kind,
                Name          = name,
                ContainerName = containerName,
                Namespace     = ns,
                Signature     = signature,
                IsPublic      = isPublic,
                DocComment    = doc,
                FullText      = node.ToFullString().Trim(),
                Language      = "C#",
                FilePath      = filePath,
            });
        }

        private static string? GetNamespace(SyntaxNode node)
            => node.Ancestors()
                   .OfType<BaseNamespaceDeclarationSyntax>()
                   .FirstOrDefault()
                   ?.Name.ToString();

        private static bool HasPublicModifier(MemberDeclarationSyntax node)
            => node.Modifiers.Any(m => m.IsKind(SyntaxKind.PublicKeyword));

        private static string? ExtractDocComment(SyntaxNode node)
        {
            var lines = node.GetLeadingTrivia()
                .Where(t => t.IsKind(SyntaxKind.SingleLineDocumentationCommentTrivia)
                         || t.IsKind(SyntaxKind.MultiLineDocumentationCommentTrivia))
                .SelectMany(t => t.ToString()
                    .Split('\n')
                    .Select(l => l.TrimStart().TrimStart('/').TrimStart('*').Trim()))
                .Where(l => l.Length > 0)
                .ToList();

            return lines.Count > 0 ? string.Join("\n", lines) : null;
        }

        private static string BuildMethodSignature(string name, ParameterListSyntax parameters, TypeSyntax returnType)
        {
            var paramStr = string.Join(", ", parameters.Parameters.Select(p => $"{p.Type} {p.Identifier}".Trim()));
            return $"{name}({paramStr}) -> {returnType}";
        }

        private static string BuildConstructorSignature(string name, ParameterListSyntax parameters)
        {
            var paramStr = string.Join(", ", parameters.Parameters.Select(p => $"{p.Type} {p.Identifier}".Trim()));
            return $"{name}({paramStr})";
        }
    }
}
