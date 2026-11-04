package models

// Source types merged into the shape types. They are never offered for new
// sources; MigrateSourceType re-types a source that still carries one (a
// saved config from before the merge, or an imported file).
const (
	LegacySourceRequirements = "requirements"
	LegacySourceTestCase     = "test-case"
	LegacySourceTestCode     = "test-code"
)

// ConfigTestFilePatterns is the per-source "Test file patterns" override of
// a Code source (see CodeKind).
const ConfigTestFilePatterns = "TestFilePatterns"

// MigrateSourceType re-types a source of a merged legacy type, reporting
// whether it changed anything:
//   - Requirements and Test Cases fetched as work items (FetchMode
//     "workitems") become Work Items; fetched as files they become
//     Documentation.
//   - Test Code becomes Code with every file a test (Test file patterns
//     "**"), so it keeps meaning exactly what it did.
//
// A migrated source has to be re-synced into its new collection, so its sync
// state is reset; its points in the legacy collection are removed by that
// re-sync.
func MigrateSourceType(src *SourceDefinition) bool {
	switch src.Type {
	case LegacySourceRequirements, LegacySourceTestCase:
		if src.GetConfig("FetchMode") == "workitems" {
			src.Type = SourceWorkItemQuery
		} else {
			src.Type = SourceDocumentation
		}
		delete(src.Config, "FetchMode")
	case LegacySourceTestCode:
		src.Type = SourceCodeRepo
		if src.Config == nil {
			src.Config = map[string]string{}
		}
		if src.Config[ConfigTestFilePatterns] == "" {
			src.Config[ConfigTestFilePatterns] = "**"
		}
	default:
		return false
	}
	src.SyncStatus = "idle"
	src.SyncError = nil
	src.SyncErrorPhase = nil
	src.LastSyncedAt = nil
	return true
}
