package models

import (
	"reflect"
	"testing"
)

func TestIsTestPath(t *testing.T) {
	cases := map[string]bool{
		"tests/Storefront.Tests/Identity/PasswordResetServiceTests.cs": true,
		"internal/refunds/service_test.go":                             true,
		"src/sync/offlineQueue.spec.ts":                                true,
		"src/components/Button.test.tsx":                               true,
		"src/test/java/com/acme/OrderServiceTest.java":                 true,
		"app/__tests__/cart.js":                                        true,
		"tests/test_orders.py":                                         true,
		`src\Storefront.Tests\CartTests.cs`:                            true,
		"src/Storefront/Identity/PasswordResetService.cs":              false,
		"internal/refunds/service.go":                                  false,
		"src/sync/offlineQueue.ts":                                     false,
		"docs/test-cases/TC-031-partial-refunds.md":                    false,
		"src/testing/helpers.go":                                       false,
		"src/attestation/verify.ts":                                    false,
	}
	for p, want := range cases {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestMatchPathPattern(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*_test.go", "internal/a/b_test.go", true}, // no folder: the file name, anywhere
		{"*_test.go", "internal/a/b.go", false},
		{"tests/**", "tests/unit/a.cs", true},
		{"tests/**", "src/tests/a.cs", false}, // with a folder: from the root
		{"**/qa/*.cs", "src/qa/A.cs", true},
		{"**/qa/*.cs", "qa/A.cs", true}, // **/ matches zero folders
		{"**/qa/*.cs", "src/qa/deep/A.cs", false},
		{"**", "anything/at/all.txt", true},
		{"Spec?.TS", "/web/spec1.ts", true}, // case-insensitive, leading slash ignored
		{"", "a.go", false},
	}
	for _, c := range cases {
		if got := MatchPathPattern(c.pattern, c.path); got != c.want {
			t.Errorf("MatchPathPattern(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestCodeKindUsesPatternsInsteadOfConventions(t *testing.T) {
	if got := CodeKind("internal/a_test.go", nil); got != CodeKindTest {
		t.Errorf("conventions: %s", got)
	}
	if got := CodeKind("src/Main.cs", nil); got != CodeKindProduction {
		t.Errorf("conventions: %s", got)
	}
	// Patterns replace the conventions: only what they match is a test.
	patterns := ParseTestPatterns("qa/**,\n *.check.js ")
	if !reflect.DeepEqual(patterns, []string{"qa/**", "*.check.js"}) {
		t.Fatalf("ParseTestPatterns = %v", patterns)
	}
	kind := CodeKindFunc(patterns)
	for p, want := range map[string]string{
		"qa/smoke/Login.cs":  CodeKindTest,
		"web/cart.check.js":  CodeKindTest,
		"internal/a_test.go": CodeKindProduction,
	} {
		if got := kind(p); got != want {
			t.Errorf("kind(%q) = %s, want %s", p, got, want)
		}
	}
}

func TestMigrateSourceType(t *testing.T) {
	cases := []struct {
		src      SourceDefinition
		wantType string
		wantCfg  map[string]string
	}{
		{SourceDefinition{Type: LegacySourceRequirements, Config: map[string]string{"FetchMode": "workitems", "WorkItemTypes": "Product Requirement"}},
			SourceWorkItemQuery, map[string]string{"WorkItemTypes": "Product Requirement"}},
		{SourceDefinition{Type: LegacySourceTestCase, Config: map[string]string{"FetchMode": "files", "PathFilter": "*.md"}},
			SourceDocumentation, map[string]string{"PathFilter": "*.md"}},
		{SourceDefinition{Type: LegacySourceRequirements}, SourceDocumentation, nil},
		{SourceDefinition{Type: LegacySourceTestCode, Config: map[string]string{"PathFilter": "*.cs"}},
			SourceCodeRepo, map[string]string{"PathFilter": "*.cs", ConfigTestFilePatterns: "**"}},
		{SourceDefinition{Type: LegacySourceTestCode, Config: map[string]string{ConfigTestFilePatterns: "qa/**"}},
			SourceCodeRepo, map[string]string{ConfigTestFilePatterns: "qa/**"}},
	}
	for _, c := range cases {
		src := c.src
		msg := "synced"
		src.SyncStatus, src.SyncError = "completed", &msg
		if !MigrateSourceType(&src) {
			t.Errorf("%s: not migrated", c.src.Type)
			continue
		}
		if src.Type != c.wantType {
			t.Errorf("%s → %s, want %s", c.src.Type, src.Type, c.wantType)
		}
		if len(c.wantCfg) > 0 && !reflect.DeepEqual(src.Config, c.wantCfg) {
			t.Errorf("%s config = %v, want %v", c.src.Type, src.Config, c.wantCfg)
		}
		if src.SyncStatus != "idle" || src.SyncError != nil || src.LastSyncedAt != nil {
			t.Errorf("%s: sync state not reset for the re-sync: %+v", c.src.Type, src)
		}
	}
	current := SourceDefinition{Type: SourceCodeRepo, SyncStatus: "completed"}
	if MigrateSourceType(&current) || current.SyncStatus != "completed" {
		t.Error("a current type must be left alone")
	}
}
