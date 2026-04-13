from app.models import CodeUnit, CodeUnitKind, ConfigKeys, PayloadKeys, SourceDefinition


# ── PayloadKeys ───────────────────────────────────────────────────────────────

def test_tag_prefix():
    assert PayloadKeys.tag("source_id") == "tag_source_id"

def test_prop_prefix():
    assert PayloadKeys.prop("title") == "prop_title"

def test_tag_empty_key():
    assert PayloadKeys.tag("") == "tag_"


# ── SourceDefinition ──────────────────────────────────────────────────────────

def test_id_auto_generated_as_uuid():
    s = SourceDefinition(type="workitem", name="x")
    assert len(s.id) == 36
    assert s.id.count("-") == 4

def test_two_instances_have_different_ids():
    a = SourceDefinition(type="workitem", name="x")
    b = SourceDefinition(type="workitem", name="x")
    assert a.id != b.id

def test_get_config_returns_value():
    s = SourceDefinition(type="t", name="n", config={ConfigKeys.URL: "http://x"})
    assert s.get_config(ConfigKeys.URL) == "http://x"

def test_get_config_returns_supplied_default():
    s = SourceDefinition(type="t", name="n")
    assert s.get_config("missing", "fallback") == "fallback"

def test_get_config_returns_empty_string_by_default():
    s = SourceDefinition(type="t", name="n")
    assert s.get_config("missing") == ""

def test_default_sync_status_is_idle():
    s = SourceDefinition(type="t", name="n")
    assert s.sync_status == "idle"


# ── CodeUnit helpers ──────────────────────────────────────────────────────────

def _unit(**overrides) -> CodeUnit:
    defaults = dict(
        kind=CodeUnitKind.METHOD,
        name="MyMethod",
        full_text="void MyMethod() {}",
        language="C#",
        file_path="src/Foo.cs",
    )
    defaults.update(overrides)
    return CodeUnit(**defaults)


# ── CodeUnit.enriched_text ────────────────────────────────────────────────────

def test_enriched_text_always_includes_language():
    assert "Language: C#" in _unit().enriched_text

def test_enriched_text_always_includes_file():
    assert "File: src/Foo.cs" in _unit().enriched_text

def test_enriched_text_includes_kind_and_name():
    assert "Method: MyMethod" in _unit().enriched_text

def test_enriched_text_includes_container_in_label():
    assert "Method: MyClass.MyMethod" in _unit(container_name="MyClass").enriched_text

def test_enriched_text_includes_namespace_when_set():
    assert "Namespace: MyApp.Core" in _unit(namespace="MyApp.Core").enriched_text

def test_enriched_text_omits_namespace_when_absent():
    assert "Namespace:" not in _unit().enriched_text

def test_enriched_text_includes_signature_when_set():
    assert "Signature: (int x)" in _unit(signature="(int x)").enriched_text

def test_enriched_text_includes_docs_when_set():
    assert "Docs: Does the thing" in _unit(doc_comment="Does the thing").enriched_text

def test_enriched_text_ends_with_full_text():
    assert _unit().enriched_text.endswith("void MyMethod() {}")


# ── CodeUnit.to_id_slug ───────────────────────────────────────────────────────

def test_slug_lowercased_name():
    assert _unit(name="DoWork").to_id_slug() == "dowork"

def test_slug_with_container_prepended():
    slug = _unit(name="Execute", container_name="TaskRunner").to_id_slug()
    assert slug.startswith("taskrunner-execute")

def test_slug_with_signature_appends_8_char_hex():
    slug = _unit(name="Run", signature="(string arg)").to_id_slug()
    parts = slug.split("-")
    assert len(parts[-1]) == 8

def test_slug_special_chars_replaced_with_dash():
    slug = _unit(name="My Method!").to_id_slug()
    assert " " not in slug
    assert "!" not in slug

def test_slug_truncated_at_80_chars():
    assert len(_unit(name="a" * 100).to_id_slug()) <= 80

def test_slug_is_deterministic():
    u = _unit(name="Foo", signature="()")
    assert u.to_id_slug() == u.to_id_slug()
