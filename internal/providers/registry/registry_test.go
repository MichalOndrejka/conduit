package registry

import (
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

type fakeProviders map[string]models.Provider

func (f fakeProviders) Get(id string) (*models.Provider, error) {
	if p, ok := f[id]; ok {
		return &p, nil
	}
	return nil, nil
}

func (f fakeProviders) ListAll() ([]models.Provider, error) {
	var out []models.Provider
	for _, p := range f {
		out = append(out, p)
	}
	return out, nil
}

type fakeSecrets map[string]string

func (f fakeSecrets) GetValue(name string) string { return f[name] }

func resolver() *Resolver {
	return &Resolver{
		Providers: fakeProviders{
			"ado": {ID: "ado", Name: "ADO", Kind: "ado", BaseURL: "https://dev.azure.com/", APIVersion: "6.0", SkipTLSVerify: true},
			"gh":  {ID: "gh", Name: "GH", Kind: "github", BaseURL: "https://github.example.com"},
		},
		Secrets: fakeSecrets{models.ProviderTokenName("ado"): "pat"},
	}
}

func TestEffectiveAdoFiles(t *testing.T) {
	r := resolver()
	src := &models.SourceDefinition{ID: "s", Name: "Code", Type: models.SourceCodeRepo, ProviderID: "ado",
		Config: map[string]string{"Organization": "contoso", "Project": "My Proj", "Repo": "api", "Branch": "dev", "PathFilter": "/src/**"}}
	eff, err := r.Effective(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Provider": "api", "Platform": "ado", "AuthType": "basic", "Password": models.ProviderTokenName("ado"),
		"AdoOrg": "https://dev.azure.com/contoso", "AdoProject": "My Proj", "AdoRepo": "api", "AdoApiVersion": "6.0",
		"VerifySSL": "false", "Branch": "dev", "PathFilter": "/src/**",
		"Url": "https://dev.azure.com/contoso/My%20Proj/_apis/git/repositories/api/items?api-version=6.0",
	}
	for k, v := range want {
		if eff.Config[k] != v {
			t.Errorf("%s = %q, want %q", k, eff.Config[k], v)
		}
	}
	if r.GetValue(eff.Config["Password"]) != "pat" {
		t.Error("the compiled credential name doesn't resolve to the provider's token")
	}
	if src.Config["Url"] != "" {
		t.Error("Effective changed the stored source")
	}
}

func TestEffectiveAdoLiveAndCommits(t *testing.T) {
	r := resolver()
	pr, _ := r.Effective(&models.SourceDefinition{Type: models.SourcePullRequest, ProviderID: "ado",
		Config: map[string]string{"Project": "P", "Repositories": "a,b"}})
	if pr.Config["Url"] != "https://dev.azure.com/P" || pr.Config["Repositories"] != "a,b" {
		t.Errorf("pull requests = %+v", pr.Config)
	}
	commits, _ := r.Effective(&models.SourceDefinition{Type: models.SourceGitCommits, ProviderID: "ado",
		Config: map[string]string{"Project": "P", "Repo": "r"}})
	if !strings.HasSuffix(commits.Config["Url"], "/P/_apis/git/repositories/r/commits?api-version=6.0") {
		t.Errorf("commits Url = %q", commits.Config["Url"])
	}
}

func TestEffectiveGitHub(t *testing.T) {
	eff, err := resolver().Effective(&models.SourceDefinition{Type: models.SourceWorkItemQuery, ProviderID: "gh",
		Config: map[string]string{"Organization": "acme", "Repo": "app", "Labels": "bug"}})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"Provider": "github", "GitHubUrl": "https://github.example.com", "GitHubOwner": "acme",
		"GitHubRepo": "app", "Token": models.ProviderTokenName("gh"), "Labels": "bug"} {
		if eff.Config[k] != v {
			t.Errorf("%s = %q, want %q", k, eff.Config[k], v)
		}
	}
}

func TestEffectiveErrors(t *testing.T) {
	r := resolver()
	if _, err := r.Effective(&models.SourceDefinition{Name: "Old"}); err == nil || !strings.Contains(err.Error(), "recreated") {
		t.Errorf("legacy source: err = %v", err)
	}
	if _, err := r.Effective(&models.SourceDefinition{Name: "Gone", ProviderID: "x"}); err == nil {
		t.Error("missing provider accepted")
	}
	all := r.EffectiveAll([]models.SourceDefinition{{Name: "Old"}, {Type: models.SourcePullRequest, ProviderID: "gh", Config: map[string]string{}}})
	if len(all) != 1 {
		t.Errorf("EffectiveAll kept %d sources, want only the resolvable one", len(all))
	}
}
