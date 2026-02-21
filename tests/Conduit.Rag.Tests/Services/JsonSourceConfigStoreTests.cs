using Conduit.Rag.Models;
using Conduit.Rag.Services;
using NUnit.Framework;

namespace Conduit.Rag.Tests.Services;

[TestFixture]
public class JsonSourceConfigStoreTests
{
    private string _filePath = null!;
    private JsonSourceConfigStore _store = null!;

    [SetUp]
    public void SetUp()
    {
        _filePath = Path.Combine(Path.GetTempPath(), $"conduit-test-{Guid.NewGuid():N}.json");
        _store    = new JsonSourceConfigStore(_filePath);
    }

    [TearDown]
    public void TearDown()
    {
        if (File.Exists(_filePath))
            File.Delete(_filePath);
    }

    // ─── GetAllAsync ─────────────────────────────────────────────

    [Test]
    public async Task GetAllAsync_WhenFileDoesNotExist_ReturnsEmptyList()
    {
        var result = await _store.GetAllAsync();

        Assert.That(result, Is.Empty);
    }

    [Test]
    public async Task GetAllAsync_AfterSave_ReturnsAllSources()
    {
        await _store.SaveAsync(MakeSource("a", "Source A"));
        await _store.SaveAsync(MakeSource("b", "Source B"));

        var result = await _store.GetAllAsync();

        Assert.That(result, Has.Count.EqualTo(2));
    }

    // ─── GetByIdAsync ────────────────────────────────────────────

    [Test]
    public async Task GetByIdAsync_ReturnsMatchingSource()
    {
        var source = MakeSource("target", "My Source");
        await _store.SaveAsync(source);

        var result = await _store.GetByIdAsync("target");

        Assert.That(result,       Is.Not.Null);
        Assert.That(result!.Name, Is.EqualTo("My Source"));
    }

    [Test]
    public async Task GetByIdAsync_UnknownId_ReturnsNull()
    {
        var result = await _store.GetByIdAsync("does-not-exist");

        Assert.That(result, Is.Null);
    }

    // ─── SaveAsync ───────────────────────────────────────────────

    [Test]
    public async Task SaveAsync_ExistingId_UpdatesInPlace()
    {
        await _store.SaveAsync(MakeSource("x", "Original"));

        var updated = MakeSource("x", "Updated");
        await _store.SaveAsync(updated);

        var all = await _store.GetAllAsync();

        Assert.That(all, Has.Count.EqualTo(1));
        Assert.That(all[0].Name, Is.EqualTo("Updated"));
    }

    [Test]
    public async Task SaveAsync_PersistsConfigDictionary()
    {
        var source = MakeSource("cfg", "Config Source");
        source.Config["organization"] = "myorg";
        source.Config["pat"]          = "secret";
        await _store.SaveAsync(source);

        var loaded = await _store.GetByIdAsync("cfg");

        Assert.That(loaded!.Config["organization"], Is.EqualTo("myorg"));
        Assert.That(loaded.Config["pat"],           Is.EqualTo("secret"));
    }

    // ─── DeleteAsync ─────────────────────────────────────────────

    [Test]
    public async Task DeleteAsync_RemovesSourceFromStore()
    {
        await _store.SaveAsync(MakeSource("keep", "Keep"));
        await _store.SaveAsync(MakeSource("del",  "Delete me"));

        await _store.DeleteAsync("del");

        var all = await _store.GetAllAsync();

        Assert.That(all, Has.Count.EqualTo(1));
        Assert.That(all[0].Id, Is.EqualTo("keep"));
    }

    // ─── Helpers ─────────────────────────────────────────────────

    private static SourceDefinition MakeSource(string id, string name) => new()
    {
        Id      = id,
        Name    = name,
        Type    = SourceTypes.ManualDocument,
        Enabled = true
    };
}
