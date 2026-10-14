// Filter fields for fetched documents — real-world categories from the
// source system (project, repository, path, author, …) that the MCP
// search tools can narrow results by. See models/filters.go for the field
// set and normalization.
package sources

import (
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// adoScope returns the Azure DevOps project and repository an API URL points
// at. The ADO preset's own project/repo fields win (they hold display names,
// whereas a URL may carry a GUID); otherwise both are read from the URL path
// ("…/{project}/_apis/git/repositories/{repo}/…"). The project is only taken
// from the URL when at least two segments precede _apis, since a single one
// is the organization or collection, not a project.
func (a *APISource) adoScope(apiURL string) (project, repo string) {
	project = a.src.GetConfig("AdoProject")
	repo = a.src.GetConfig("AdoRepo")
	if project != "" && repo != "" {
		return project, repo
	}
	u, err := url.Parse(apiURL)
	if err != nil {
		return project, repo
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, s := range segs {
		if s != "_apis" {
			continue
		}
		if project == "" && i >= 2 {
			project, _ = url.PathUnescape(segs[i-1])
		}
		if repo == "" && i+3 < len(segs) && segs[i+1] == "git" && segs[i+2] == "repositories" {
			repo, _ = url.PathUnescape(segs[i+3])
		}
		break
	}
	return project, repo
}

// scopeFilters returns a document's project/repository filters (and mirrors
// them into props, so results show the values an agent can filter by).
func (a *APISource) scopeFilters(apiURL string, props map[string]string) models.DocumentFilters {
	var f models.DocumentFilters
	project, repo := a.adoScope(apiURL)
	if project != "" {
		f.Add(models.FilterProject, project)
		props["project"] = project
	}
	if repo != "" {
		f.Add(models.FilterRepository, repo)
		props["repository"] = repo
	}
	return f
}

// addFileFilters adds a repo file's path and extension.
func addFileFilters(f *models.DocumentFilters, p string) {
	f.Add(models.FilterPath, p)
	if ext := strings.TrimPrefix(path.Ext(p), "."); ext != "" {
		f.Add(models.FilterExtension, ext)
	}
}

// identityValues extracts the names of an ADO identity field, which arrives
// either as an object ({displayName, uniqueName}) or a plain string.
func identityValues(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case map[string]any:
		var out []string
		for _, k := range []string{"displayName", "uniqueName"} {
			if s, ok := t[k].(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// parseTime parses an RFC 3339 timestamp, returning the zero time on failure.
func parseTime(v any) time.Time {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
