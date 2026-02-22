namespace Conduit.Rag.Parsing;

public enum CodeUnitKind
{
    File,
    Namespace,
    Class,
    Interface,
    Record,
    Struct,
    Enum,
    Method,
    Constructor,
    Property,
    Function,
    Type,
    Section,
}

public sealed class CodeUnit
{
    public CodeUnitKind Kind          { get; init; }
    public string       Name          { get; init; } = "";
    public string?      ContainerName { get; init; }
    public string?      Namespace     { get; init; }
    public string?      Signature     { get; init; }
    public bool         IsPublic      { get; init; }
    public string?      DocComment    { get; init; }
    public string       FullText      { get; init; } = "";
    public string       Language      { get; init; } = "";
    public string       FilePath      { get; init; } = "";

    public string EnrichedText => BuildEnrichedText(this);

    public static string BuildEnrichedText(CodeUnit unit)
    {
        var sb = new System.Text.StringBuilder();

        sb.AppendLine($"// Language: {unit.Language}");
        sb.AppendLine($"// File: {unit.FilePath}");

        if (!string.IsNullOrEmpty(unit.Namespace))
            sb.AppendLine($"// Namespace: {unit.Namespace}");

        if (!string.IsNullOrEmpty(unit.ContainerName))
            sb.AppendLine($"// Type: {unit.ContainerName}");

        if (!string.IsNullOrEmpty(unit.Signature))
            sb.AppendLine($"// Member: {unit.Signature}");
        else if (!string.IsNullOrEmpty(unit.Name))
            sb.AppendLine($"// Name: {unit.Name}");

        sb.AppendLine($"// Visibility: {(unit.IsPublic ? "public" : "internal")}");
        sb.AppendLine($"// Kind: {unit.Kind.ToString().ToLowerInvariant()}");
        sb.AppendLine();

        if (!string.IsNullOrEmpty(unit.DocComment))
            sb.AppendLine(unit.DocComment);

        sb.Append(unit.FullText);

        return sb.ToString();
    }

    /// <summary>
    /// Produces a URL-safe slug for use in document IDs.
    /// Uses a process-stable FNV-1a hash (not <c>GetHashCode</c>) so IDs are
    /// identical across application restarts, preventing duplicate Qdrant points.
    /// </summary>
    public string ToIdSlug()
    {
        var name = string.IsNullOrEmpty(ContainerName)
            ? Name
            : $"{ContainerName}.{Name}";

        var slug = System.Text.RegularExpressions.Regex.Replace(
            name.ToLowerInvariant(), @"[^a-z0-9_\-]", "-");

        // Append a short stable hash of the signature to disambiguate overloads.
        if (!string.IsNullOrEmpty(Signature))
        {
            var hash = ParserUtils.StableHash(Signature) % 100000;
            slug = $"{slug}-{hash:D5}";
        }

        return slug;
    }
}
