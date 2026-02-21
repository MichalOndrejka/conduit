using NUnit.Framework;
using Qdrant.Demo.Api.Extensions;

namespace Qdrant.Demo.Api.Tests;

[TestFixture]
public class DateTimeExtensionsTests
{
    [Test]
    public void ToUnixMs_ReturnsZeroForEpoch()
    {
        var epoch = new DateTime(1970, 1, 1, 0, 0, 0, DateTimeKind.Utc);
        Assert.That(epoch.ToUnixMs(), Is.EqualTo(0));
    }

    [Test]
    public void ToUnixMs_KnownTimestamp()
    {
        var dt = new DateTime(2025, 1, 1, 0, 0, 0, DateTimeKind.Utc);
        Assert.That(dt.ToUnixMs(), Is.EqualTo(1735689600000L));
    }
}
