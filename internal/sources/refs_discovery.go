// Discovery of a repository's branches and tags, so a source ticks the
// versions to embed from what the repository actually has.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// RepoRefs is a repository's branches and tags. Branches list the default
// branch first, then by name; tags newest version first (v10 before v9).
type RepoRefs struct {
	DefaultBranch string   `json:"default"`
	Branches      []string `json:"branches"`
	Tags          []string `json:"tags"`
}

func (r *RepoRefs) sort() {
	sort.Slice(r.Branches, func(i, j int) bool {
		a, b := r.Branches[i], r.Branches[j]
		if (a == r.DefaultBranch) != (b == r.DefaultBranch) {
			return a == r.DefaultBranch
		}
		return naturalLess(a, b)
	})
	sort.Slice(r.Tags, func(i, j int) bool { return naturalLess(r.Tags[j], r.Tags[i]) })
}

// naturalLess compares names with their runs of digits as numbers, so
// v1.10 sorts after v1.9.
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		ca, ra := leadingChunk(a)
		cb, rb := leadingChunk(b)
		if ca != cb {
			da, db := unicode.IsDigit(rune(ca[0])), unicode.IsDigit(rune(cb[0]))
			if da && db {
				na, nb := strings.TrimLeft(ca, "0"), strings.TrimLeft(cb, "0")
				if len(na) != len(nb) {
					return len(na) < len(nb)
				}
				if na != nb {
					return na < nb
				}
			} else {
				return strings.ToLower(ca) < strings.ToLower(cb)
			}
		}
		a, b = ra, rb
	}
	return len(a) < len(b)
}

// leadingChunk splits off s's leading run of digits or of non-digits.
func leadingChunk(s string) (chunk, rest string) {
	digit := unicode.IsDigit(rune(s[0]))
	i := 1
	for i < len(s) && unicode.IsDigit(rune(s[i])) == digit {
		i++
	}
	return s[:i], s[i:]
}

// AdoRepoRefs lists the branches and tags of src's Azure DevOps repository
// (AdoOrg, AdoProject, AdoRepo, AdoApiVersion and the Password credential).
func AdoRepoRefs(ctx context.Context, src *models.SourceDefinition, store secrets.Reader) (RepoRefs, error) {
	org := strings.TrimRight(strings.TrimSpace(src.GetConfig("AdoOrg")), "/")
	project := strings.TrimSpace(src.GetConfig("AdoProject"))
	repo := strings.TrimSpace(src.GetConfig("AdoRepo"))
	if org == "" || project == "" || repo == "" {
		return RepoRefs{}, fmt.Errorf("an Azure DevOps organization URL, project and repository are required")
	}
	apiVersion := strings.TrimSpace(src.GetConfig("AdoApiVersion"))
	if apiVersion == "" {
		apiVersion = "7.1"
	}
	a := &APISource{src: src, secrets: store}
	client := a.httpClient()
	repoBase := org + "/" + url.PathEscape(project) + "/_apis/git/repositories/" + url.PathEscape(repo)
	query := "?api-version=" + url.QueryEscape(apiVersion)

	var out RepoRefs
	// An empty repository has no default branch; it still lists no refs.
	if b, err := a.adoDefaultBranch(ctx, client, repoBase, query); err == nil {
		out.DefaultBranch = b
	} else if !strings.Contains(err.Error(), "no default branch") {
		return RepoRefs{}, err
	}
	list := func(filter, prefix string) ([]string, error) {
		var names []string
		token := ""
		for {
			reqURL := repoBase + "/refs" + query + "&filter=" + url.QueryEscape(filter)
			if token != "" {
				reqURL += "&continuationToken=" + url.QueryEscape(token)
			}
			data, header, err := a.getRawWithHeader(ctx, client, reqURL)
			if err != nil {
				return nil, err
			}
			var page struct {
				Value []struct {
					Name string `json:"name"`
				} `json:"value"`
			}
			if err := json.Unmarshal(data, &page); err != nil {
				return nil, fmt.Errorf("refs response is not valid JSON: %w", err)
			}
			for _, r := range page.Value {
				if n := strings.TrimPrefix(r.Name, prefix); n != "" && n != r.Name {
					names = append(names, n)
				}
			}
			if token = header.Get("x-ms-continuationtoken"); token == "" {
				return names, nil
			}
		}
	}
	var err error
	if out.Branches, err = list("heads/", "refs/heads/"); err != nil {
		return RepoRefs{}, fmt.Errorf("listing branches: %w", err)
	}
	if out.Tags, err = list("tags/", "refs/tags/"); err != nil {
		return RepoRefs{}, fmt.Errorf("listing tags: %w", err)
	}
	out.sort()
	return out, nil
}

// GitHubRepoRefs lists the branches and tags of src's GitHub repository.
func GitHubRepoRefs(ctx context.Context, src *models.SourceDefinition, store secrets.Reader) (RepoRefs, error) {
	g, err := newGitHubSource(src, store)
	if err != nil {
		return RepoRefs{}, err
	}
	var out RepoRefs
	if out.DefaultBranch, err = g.defaultBranch(ctx); err != nil && !strings.Contains(err.Error(), "no default branch") {
		return RepoRefs{}, err
	}
	list := func(what string) ([]string, error) {
		var names []string
		err := g.pages(ctx, g.repoAPI()+"/"+what, func(data []byte) (int, bool, error) {
			var page []struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(data, &page); err != nil {
				return 0, false, fmt.Errorf("listing %s: %w", what, err)
			}
			for _, r := range page {
				if r.Name != "" {
					names = append(names, r.Name)
				}
			}
			return len(page), true, nil
		})
		return names, err
	}
	if out.Branches, err = list("branches"); err != nil {
		return RepoRefs{}, fmt.Errorf("listing branches of %s/%s: %w", g.owner, g.repo, err)
	}
	if out.Tags, err = list("tags"); err != nil {
		return RepoRefs{}, fmt.Errorf("listing tags of %s/%s: %w", g.owner, g.repo, err)
	}
	out.sort()
	return out, nil
}
