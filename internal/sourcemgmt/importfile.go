package sourcemgmt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// importFields are the fields of one source in an export file (see
// store.ExportedSource).
var importFields = []string{"id", "type", "name", "disabled", "config"}

// legacySyncFields are sync state that exports made before it was left out
// still carry, in snake_case and the older camelCase. They describe the
// exporting machine's index, so they are accepted and dropped.
var legacySyncFields = []string{
	"last_synced_at", "sync_status", "sync_error", "sync_error_phase", "identity",
	"lastSyncedAt", "syncStatus", "syncError", "syncErrorPhase",
}

// ParseImport reads an export file strictly: a JSON array of sources (or
// the older {"sources": [...]} wrapper), each with only the export fields,
// a current source type, a known provider, only that provider's config keys,
// and a config that passes Validate — the rules the source form and the
// source_manage tool apply. Any problem fails the whole file, naming every
// bad source, so nothing is imported. Credentials need not exist yet: the
// Credentials page lists the missing ones.
func ParseImport(data []byte) ([]models.SourceDefinition, error) {
	raws, err := importList(data)
	if err != nil {
		return nil, err
	}
	if len(raws) == 0 {
		return nil, errors.New("the file has no sources")
	}
	var out []models.SourceDefinition
	var problems []string
	for i, raw := range raws {
		src, err := parseImportedSource(raw)
		if err != nil {
			label := fmt.Sprintf("source %d", i+1)
			if src.Name != "" {
				label += fmt.Sprintf(" (%s)", src.Name)
			}
			problems = append(problems, label+": "+err.Error())
			continue
		}
		out = append(out, src)
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}

// importList returns the file's sources: a top-level array, or the older
// wrapper object with a "sources" array and nothing else.
func importList(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &wrapper); err != nil {
			return nil, fmt.Errorf("not valid JSON: %w", err)
		}
		for key := range wrapper {
			if key != "sources" {
				return nil, fmt.Errorf("unexpected top-level field %q: an export is an array of sources", key)
			}
		}
		trimmed = wrapper["sources"]
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(trimmed, &raws); err != nil {
		return nil, fmt.Errorf("not an export file: expected a JSON array of sources (%v)", err)
	}
	return raws, nil
}

// parseImportedSource checks one source. The returned definition carries the
// name even on error, so the error can say which source failed.
func parseImportedSource(raw json.RawMessage) (models.SourceDefinition, error) {
	var src models.SourceDefinition
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return src, errors.New("not a JSON object")
	}
	_ = json.Unmarshal(fields["name"], &src.Name) // for the error label only
	for key := range fields {
		if !slices.Contains(importFields, key) && !slices.Contains(legacySyncFields, key) {
			return src, fmt.Errorf("unexpected field %q", key)
		}
	}
	decode := func(key string, into any) error {
		v, ok := fields[key]
		if !ok {
			return nil
		}
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.DisallowUnknownFields()
		if err := dec.Decode(into); err != nil {
			return fmt.Errorf("field %q: %v", key, err)
		}
		return nil
	}
	if err := errors.Join(
		decode("id", &src.ID),
		decode("type", &src.Type),
		decode("name", &src.Name),
		decode("disabled", &src.Disabled),
		decode("config", &src.Config),
	); err != nil {
		return src, err
	}
	src.Name = strings.TrimSpace(src.Name)

	if src.Name == "" {
		return src, errors.New("name is required")
	}
	switch {
	case src.Type == models.RetiredSourceTestCase:
		return src, errors.New(`type "test-case" no longer exists: test cases are Requirements sources now`)
	case src.Type == models.RetiredSourceWorkItem:
		return src, errors.New(`type "work-item" was renamed: use "backlog"`)
	case !ValidType(src.Type):
		values := make([]string, len(Types))
		for i, t := range Types {
			values[i] = t.Value
		}
		return src, fmt.Errorf("unknown type %q; types are %s", src.Type, strings.Join(values, ", "))
	}
	if src.Config == nil {
		return src, errors.New("config is required")
	}
	provider := src.Config["Provider"]
	if !slices.Contains(Providers, provider) {
		return src, fmt.Errorf("config.Provider must be one of %s, got %q", strings.Join(Providers, ", "), provider)
	}
	allowed := ConfigKeys(provider)
	for key := range src.Config {
		if key != "Provider" && !slices.Contains(allowed, key) {
			return src, fmt.Errorf("config key %q is not allowed for provider %q", key, provider)
		}
	}
	if msg := Validate(src.Type, src.Config); msg != "" {
		return src, errors.New(msg)
	}
	return src, nil
}
