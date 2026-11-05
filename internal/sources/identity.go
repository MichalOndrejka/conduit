package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// IdentitySource is a source that can name its token's owner. The sync
// records the identity on the source so a "me" filter resolves from the
// cache, without a request at query time.
type IdentitySource interface {
	FetchIdentity(ctx context.Context) ([]string, error)
}

// FetchIdentity reads the token owner from Azure DevOps' connectionData:
// display name, unique name (email or DOMAIN\user) and account. A generic
// API source without an Azure DevOps organization has no identity.
func (a *APISource) FetchIdentity(ctx context.Context) ([]string, error) {
	org := strings.TrimRight(a.src.GetConfig("AdoOrg"), "/")
	if org == "" {
		return nil, nil
	}
	data, err := a.getRaw(ctx, a.httpClient(), org+"/_apis/connectionData")
	if err != nil {
		return nil, err
	}
	var resp struct {
		AuthenticatedUser struct {
			ProviderDisplayName string `json:"providerDisplayName"`
			CustomDisplayName   string `json:"customDisplayName"`
			Properties          map[string]struct {
				Value any `json:"$value"`
			} `json:"properties"`
		} `json:"authenticatedUser"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("connectionData response is not valid JSON: %w", err)
	}
	u := resp.AuthenticatedUser
	ids := []string{u.ProviderDisplayName, u.CustomDisplayName}
	for _, key := range []string{"Account", "Mail"} {
		if p, ok := u.Properties[key]; ok {
			if s, ok := p.Value.(string); ok {
				ids = append(ids, s)
			}
		}
	}
	return compactStrings(ids), nil
}

// FetchIdentity reads the token owner from GitHub's /user: login, name and
// public email.
func (g *GitHubSource) FetchIdentity(ctx context.Context) ([]string, error) {
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if _, err := g.getJSON(ctx, g.apiBase+"/user", &u); err != nil {
		return nil, err
	}
	return compactStrings([]string{u.Login, u.Name, u.Email}), nil
}

func compactStrings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	return out
}
