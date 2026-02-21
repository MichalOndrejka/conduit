using Conduit.Rag.Extensions;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Extensions;

[TestFixture]
public class DateTimeExtensionsTests
{
    // ─── ToUnixMs ───────────────────────────────────────────────

    [Test]
    public void ToUnixMs_Epoch_ReturnsZero()
    {
        var epoch = new DateTime(1970, 1, 1, 0, 0, 0, DateTimeKind.Utc);

        Assert.That(epoch.ToUnixMs(), Is.EqualTo(0L));
    }

    [Test]
    public void ToUnixMs_KnownUtcDate_ReturnsExpectedValue()
    {
        // 2025-01-01 00:00:00 UTC = 1735689600000 ms since epoch
        var dt = new DateTime(2025, 1, 1, 0, 0, 0, DateTimeKind.Utc);

        Assert.That(dt.ToUnixMs(), Is.EqualTo(1_735_689_600_000L));
    }

    [Test]
    public void ToUnixMs_LocalDateTime_IsConvertedToUtcBeforeCalculation()
    {
        var utcNow   = DateTime.UtcNow;
        var localNow = utcNow.ToLocalTime();

        Assert.That(localNow.ToUnixMs(), Is.EqualTo(utcNow.ToUnixMs()).Within(1));
    }
}
