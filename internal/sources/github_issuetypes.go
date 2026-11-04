// Discovery of a GitHub organization's issue types (Bug, Feature, Task or
// whatever its admins defined), the GitHub counterpart of an Azure DevOps
// project's work item types.
package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// GitHubIssueTypes lists the enabled issue types of src's owner, sorted by
// name. available is false when the owner has none to offer: a personal
// account, or a GitHub Enterprise Server version without issue types, both
// of which answer 404.
func GitHubIssueTypes(ctx context.Context, src *models.SourceDefinition, store secrets.Reader) (types []string, available bool, err error) {
	g, err := newGitHubSource(src, store)
	if err != nil {
		return nil, false, err
	}
	var resp []struct {
		Name      string `json:"name"`
		IsEnabled *bool  `json:"is_enabled"`
	}
	if _, err := g.getJSON(ctx, g.apiBase+"/orgs/"+url.PathEscape(g.owner)+"/issue-types", &resp); err != nil {
		var status *httpStatusError
		if errors.As(err, &status) && status.status == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("listing issue types of %s: %w", g.owner, err)
	}
	for _, t := range resp {
		if t.Name != "" && (t.IsEnabled == nil || *t.IsEnabled) {
			types = append(types, t.Name)
		}
	}
	sort.Strings(types)
	return types, true, nil
}
