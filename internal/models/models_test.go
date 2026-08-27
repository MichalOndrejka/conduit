package models

import "testing"

func TestGetConfigReturnsEmptyStringForNilConfig(t *testing.T) {
	s := &SourceDefinition{}
	if got := s.GetConfig("anything"); got != "" {
		t.Errorf("GetConfig on nil Config = %q, want empty string", got)
	}
}

func TestGetConfigReturnsValueOrEmptyString(t *testing.T) {
	s := &SourceDefinition{Config: map[string]string{"branch": "main"}}
	if got := s.GetConfig("branch"); got != "main" {
		t.Errorf("GetConfig(%q) = %q, want %q", "branch", got, "main")
	}
	if got := s.GetConfig("missing"); got != "" {
		t.Errorf("GetConfig(%q) = %q, want empty string", "missing", got)
	}
}

func TestTagKeyPrefixesWithTag(t *testing.T) {
	if got := TagKey("source_id"); got != "tag_source_id" {
		t.Errorf("TagKey(%q) = %q, want %q", "source_id", got, "tag_source_id")
	}
	if got := TagKey(""); got != TagPrefix {
		t.Errorf("TagKey(%q) = %q, want %q", "", got, TagPrefix)
	}
}

func TestPropKeyPrefixesWithProp(t *testing.T) {
	if got := PropKey("guidance"); got != "prop_guidance" {
		t.Errorf("PropKey(%q) = %q, want %q", "guidance", got, "prop_guidance")
	}
	if got := PropKey(""); got != PropPrefix {
		t.Errorf("PropKey(%q) = %q, want %q", "", got, PropPrefix)
	}
}
