using System.Security.Cryptography;
using System.Text;

namespace Qdrant.Demo.Api.Extensions;

/// <summary>
/// Extension methods for <see cref="string"/>.
/// </summary>
public static class StringExtensions
{
    /// <summary>
    /// Deterministic UUID from a string (SHA-256 → 16 bytes → RFC 4122 variant + v5-like version bits).
    /// </summary>
    public static Guid ToDeterministicGuid(this string input)
    {
        var hash = SHA256.HashData(Encoding.UTF8.GetBytes(input));

        Span<byte> g = stackalloc byte[16];
        hash.AsSpan(0, 16).CopyTo(g);

        // version 5 (0101xxxx)
        g[6] = (byte)((g[6] & 0x0F) | 0x50);
        // RFC 4122 variant (10xxxxxx)
        g[8] = (byte)((g[8] & 0x3F) | 0x80);

        return new Guid(g);
    }
}
