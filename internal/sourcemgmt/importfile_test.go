package sourcemgmt

import (
	"strings"
	"testing"
)

// validDoc is one importable source: an Azure DevOps documentation source.
const validDoc = `{"id":"s1","type":"documentation","name":"Wiki","disabled":true,
	"config":{"Provider":"api","Url":"https://dev.azure.com/org/proj/_apis/wiki/wikis/w/pages","Token":"ado-pat"}}`

func TestParseImportAcceptsExportFormat(t *testing.T) {
	got, err := ParseImport([]byte("[" + validDoc + "]"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "s1" || got[0].Name != "Wiki" || !got[0].Disabled ||
		got[0].Config["Token"] != "ado-pat" {
		t.Errorf("ParseImport() = %+v", got)
	}
}

func TestParseImportAcceptsWrapperFormat(t *testing.T) {
	got, err := ParseImport([]byte(`{"sources":[` + validDoc + `]}`))
	if err != nil || len(got) != 1 {
		t.Fatalf("ParseImport() = %+v, %v", got, err)
	}
}

// Exports made before sync state was left out still import; the sync
// fields are dropped.
func TestParseImportDropsLegacySyncFields(t *testing.T) {
	old := strings.Replace(validDoc, `"disabled":true,`, `"disabled":true,
		"last_synced_at":"2026-11-24T08:00:00Z","sync_status":"completed","sync_error":null,
		"sync_error_phase":null,"identity":["someone@example.com"],"lastSyncedAt":null,`, 1)
	got, err := ParseImport([]byte("[" + old + "]"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].LastSyncedAt != nil || got[0].SyncStatus != "" || got[0].Identity != nil {
		t.Errorf("legacy sync fields were kept: %+v", got[0])
	}
}

func TestParseImportRejects(t *testing.T) {
	cases := map[string]struct{ data, want string }{
		"not JSON":            {`not json`, "expected a JSON array"},
		"empty":               {`[]`, "no sources"},
		"entry not an object": {`[1]`, "not a JSON object"},
		"wrapper extra field": {`{"sources":[],"version":1}`, `unexpected top-level field "version"`},
		"unknown field":       {`[{"type":"documentation","name":"A","extra":1,"config":{}}]`, `unexpected field "extra"`},
		"test-case type":      {`[{"type":"test-case","name":"Old cases","config":{"Provider":"api"}}]`, `"test-case" no longer exists`},
		"work-item type":      {`[{"type":"work-item","name":"Old items","config":{"Provider":"api"}}]`, `use "backlog"`},
		"unknown type":        {`[{"type":"wiki","name":"A","config":{"Provider":"api"}}]`, `unknown type "wiki"`},
		"missing name":        {`[{"type":"documentation","config":{"Provider":"api"}}]`, "name is required"},
		"missing config":      {`[{"type":"documentation","name":"A"}]`, "config is required"},
		"no provider":         {`[{"type":"documentation","name":"A","config":{"Url":"https://x.example"}}]`, "config.Provider must be one of"},
		"wrong field type":    {`[{"type":"documentation","name":"A","disabled":"yes","config":{"Provider":"api"}}]`, `field "disabled"`},
		"non-string config":   {`[{"type":"documentation","name":"A","config":{"Provider":"api","Top":5}}]`, `field "config"`},
		"key of other provider": {`[{"type":"documentation","name":"A","config":{"Provider":"github",` +
			`"GitHubUrl":"https://github.com","GitHubOwner":"o","GitHubRepo":"r","AdoOrg":"x"}}]`, `config key "AdoOrg" is not allowed`},
		"invalid config": {`[{"type":"documentation","name":"A","config":{"Provider":"api","Url":"not a url"}}]`, "URL must be a valid"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseImport([]byte(tc.data))
			if err == nil {
				t.Fatalf("ParseImport() = %+v, want an error", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// One bad source fails the whole file, and the error names every bad one.
func TestParseImportFailsWholeFileNamingEachBadSource(t *testing.T) {
	data := `[` + validDoc + `,
		{"type":"test-case","name":"Cases A","config":{"Provider":"api"}},
		{"type":"documentation","name":"Wiki B","bogus":true,"config":{}}]`
	got, err := ParseImport([]byte(data))
	if err == nil || got != nil {
		t.Fatalf("ParseImport() = %+v, %v; want no sources and an error", got, err)
	}
	for _, want := range []string{"source 2 (Cases A)", "source 3 (Wiki B)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
