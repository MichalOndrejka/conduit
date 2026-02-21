using System.Security.Cryptography;
using System.Text;

namespace Conduit.Rag.Extensions;

public static class StringExtensions
{
    /// <summary>Deterministic UUID from a string (SHA-256 → 16 bytes → RFC 4122 v5-like).</summary>
    public static Guid ToDeterministicGuid(this string input)
    {
        var hash = SHA256.HashData(Encoding.UTF8.GetBytes(input));

        Span<byte> g = stackalloc byte[16];
        hash.AsSpan(0, 16).CopyTo(g);

        g[6] = (byte)((g[6] & 0x0F) | 0x50); // version 5
        g[8] = (byte)((g[8] & 0x3F) | 0x80); // RFC 4122 variant

        return new Guid(g);
    }
}
