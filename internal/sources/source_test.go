package sources

import (
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestCollectionForRoutesByShape(t *testing.T) {
	cases := []struct {
		src  models.SourceDefinition
		want string
	}{
		{models.SourceDefinition{Type: models.SourceWorkItemQuery}, models.CollectionWorkItems},
		{models.SourceDefinition{Type: models.SourceCodeRepo}, models.CollectionCode},
		{models.SourceDefinition{Type: models.SourceDocumentation}, models.CollectionDocumentation},
		{models.SourceDefinition{Type: models.SourceGitCommits}, models.CollectionCommits},
		{models.SourceDefinition{Type: models.SourcePipelineRun}, models.CollectionPipelineRuns},
		// Legacy types route as they migrate.
		{models.SourceDefinition{Type: models.LegacySourceRequirements, Config: map[string]string{"FetchMode": "workitems"}}, models.CollectionWorkItems},
		{models.SourceDefinition{Type: models.LegacySourceTestCase}, models.CollectionDocumentation},
		{models.SourceDefinition{Type: models.LegacySourceTestCode}, models.CollectionCode},
	}
	for _, c := range cases {
		src := c.src
		if got := CollectionFor(&src); got != c.want {
			t.Errorf("CollectionFor(%s %v) = %s, want %s", c.src.Type, c.src.Config, got, c.want)
		}
		if src.Type != c.src.Type {
			t.Errorf("CollectionFor must not re-type the source it is given")
		}
	}
}

func fileDoc(path string) models.SourceDocument {
	d := models.SourceDocument{Properties: map[string]string{}}
	addFileFilters(&d.Filters, d.Properties, path)
	return d
}

func TestAddSourceFiltersAddsSourceProviderAndCodeKind(t *testing.T) {
	src := &models.SourceDefinition{Name: "Payments API", Type: models.SourceCodeRepo, Config: map[string]string{"Provider": "github"}}
	docs := []models.SourceDocument{fileDoc("internal/refunds/service.go"), fileDoc("internal/refunds/service_test.go")}
	AddSourceFilters(src, docs)
	for i, want := range []string{models.CodeKindProduction, models.CodeKindTest} {
		f := docs[i].Filters.Fields
		if f[models.FilterSource][0] != "payments api" || f[models.FilterProvider][0] != "github" {
			t.Errorf("doc %d source/provider = %v %v", i, f[models.FilterSource], f[models.FilterProvider])
		}
		if f[models.FilterCodeKind][0] != want || docs[i].Properties["code_kind"] != want {
			t.Errorf("doc %d code_kind = %v (prop %q), want %s", i, f[models.FilterCodeKind], docs[i].Properties["code_kind"], want)
		}
	}
	// The source's own patterns replace the conventions.
	src.Config[models.ConfigTestFilePatterns] = "qa/**"
	docs = []models.SourceDocument{fileDoc("qa/Login.cs"), fileDoc("internal/a_test.go")}
	AddSourceFilters(src, docs)
	if docs[0].Filters.Fields[models.FilterCodeKind][0] != models.CodeKindTest || docs[1].Filters.Fields[models.FilterCodeKind][0] != models.CodeKindProduction {
		t.Errorf("with patterns: %v, %v", docs[0].Filters.Fields[models.FilterCodeKind], docs[1].Filters.Fields[models.FilterCodeKind])
	}
}

func TestAddSourceFiltersOnlyClassifiesCode(t *testing.T) {
	src := &models.SourceDefinition{Name: "Docs", Type: models.SourceDocumentation}
	docs := []models.SourceDocument{fileDoc("tests/plan.md")}
	AddSourceFilters(src, docs)
	f := docs[0].Filters.Fields
	if _, ok := f[models.FilterCodeKind]; ok {
		t.Error("documentation has no code_kind")
	}
	if f[models.FilterProvider][0] != "azure-devops" || docs[0].Properties["path"] != "tests/plan.md" {
		t.Errorf("filters = %v, props = %v", f, docs[0].Properties)
	}
}
