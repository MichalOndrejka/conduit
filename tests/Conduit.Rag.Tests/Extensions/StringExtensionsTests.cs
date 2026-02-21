using Conduit.Rag.Extensions;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Extensions;

[TestFixture]
public class StringExtensionsTests
{
    // ─── ToDeterministicGuid ────────────────────────────────────

    [Test]
    public void ToDeterministicGuid_SameInput_ReturnsSameGuid()
    {
        var a = "same-input".ToDeterministicGuid();
        var b = "same-input".ToDeterministicGuid();

        Assert.That(a, Is.EqualTo(b));
    }

    [Test]
    public void ToDeterministicGuid_DifferentInputs_ReturnDifferentGuids()
    {
        var a = "input-one".ToDeterministicGuid();
        var b = "input-two".ToDeterministicGuid();

        Assert.That(a, Is.Not.EqualTo(b));
    }

    [Test]
    public void ToDeterministicGuid_IsNotEmpty()
    {
        var g = "test".ToDeterministicGuid();

        Assert.That(g, Is.Not.EqualTo(Guid.Empty));
    }

    [Test]
    public void ToDeterministicGuid_EmptyString_ReturnsDeterministicGuid()
    {
        var a = string.Empty.ToDeterministicGuid();
        var b = string.Empty.ToDeterministicGuid();

        Assert.That(a, Is.EqualTo(b));
        Assert.That(a, Is.Not.EqualTo(Guid.Empty));
    }

    [Test]
    public void ToDeterministicGuid_ProducesVersion5AndRfc4122VariantBits()
    {
        // Byte layout: guid[6] high nibble should be 0x5 (version 5)
        // guid[8] high two bits should be 10xxxxxx (variant 0x8x, 0x9x, 0xAx, 0xBx)
        var g = "bit-check".ToDeterministicGuid();
        var bytes = g.ToByteArray();

        Assert.That((bytes[6] >> 4) & 0xF, Is.EqualTo(5), "Version nibble should be 5");
        Assert.That((bytes[8] >> 6) & 0x3, Is.EqualTo(2), "Variant bits should be 10xxxxxx (RFC 4122)");
    }
}
