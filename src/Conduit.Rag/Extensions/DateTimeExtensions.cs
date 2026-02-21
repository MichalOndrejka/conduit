namespace Conduit.Rag.Extensions;

public static class DateTimeExtensions
{
    public static long ToUnixMs(this DateTime dt)
    {
        var utc = dt.Kind == DateTimeKind.Utc ? dt : dt.ToUniversalTime();
        return (long)(utc - DateTime.UnixEpoch).TotalMilliseconds;
    }
}
