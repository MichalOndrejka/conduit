namespace Conduit.Rag.Parsing;

/// <summary>Shared utilities for language parsers.</summary>
internal static class ParserUtils
{
    /// <summary>
    /// Extracts a brace-delimited block from <paramref name="lines"/> starting at
    /// <paramref name="startLine"/>, tracking { } depth.
    /// Sets <paramref name="endLine"/> to the index of the closing-brace line.
    /// If no opening brace is found, returns just the starting line and sets
    /// <paramref name="endLine"/> equal to <paramref name="startLine"/>.
    /// </summary>
    internal static string ExtractBraceBlock(string[] lines, int startLine, out int endLine)
    {
        var sb    = new System.Text.StringBuilder();
        var depth = 0;
        var found = false;

        for (var i = startLine; i < lines.Length; i++)
        {
            var line = lines[i];
            sb.AppendLine(line);

            foreach (var ch in line)
            {
                if      (ch == '{') { depth++; found = true; }
                else if (ch == '}')   depth--;
            }

            if (found && depth <= 0)
            {
                endLine = i;
                return sb.ToString().Trim();
            }
        }

        endLine = startLine;
        return lines[startLine].Trim();
    }

    /// <summary>
    /// Returns a process-stable 32-bit FNV-1a hash of <paramref name="s"/>.
    /// Unlike <c>string.GetHashCode()</c>, this is identical across process restarts.
    /// </summary>
    internal static uint StableHash(string s)
    {
        var hash = 2166136261u;
        foreach (var c in s)
        {
            hash ^= c;
            hash *= 16777619u;
        }
        return hash;
    }
}
