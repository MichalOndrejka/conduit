namespace Qdrant.Demo.Api.Extensions;

/// <summary>
/// Extension methods for <see cref="DateTime"/>.
/// </summary>
public static class DateTimeExtensions
{
    /// <summary>Convert a <see cref="DateTime"/> to Unix epoch milliseconds.</summary>
    public static long ToUnixMs(this DateTime dt)
    {
        var utc = dt.Kind == DateTimeKind.Utc ? dt : dt.ToUniversalTime();
        return (long)(utc - DateTime.UnixEpoch).TotalMilliseconds;
    }
}
