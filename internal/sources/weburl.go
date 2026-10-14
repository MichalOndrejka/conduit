// Browser (UI) links for indexed documents. Every document that can be traced
// back to a page in its source system gets a "web_url" property, so MCP search
// results can cite a clickable link to the source — never a REST API URL,
// which a person can't usefully open.
package sources

import (
	"fmt"
	"net/url"
	"strings"
)

// webURLFields are item fields that already hold a browser link in common
// REST APIs, tried in order when no WebUrlField is configured: ADO work items
// (_links.html.href), ADO commits and wiki pages (remoteUrl), ADO repos
// (webUrl), GitHub (html_url) and GitLab (web_url).
var webURLFields = []string{"_links.html.href", "_links.web.href", "remoteUrl", "webUrl", "html_url", "web_url"}

// itemWebURL returns the browser link an item carries itself — from the
// configured dot-path field if set, else the first of webURLFields present.
// API URLs (".../_apis/...") are rejected so they never pass for a UI link.
func itemWebURL(item any, configured string) string {
	fields := webURLFields
	if configured != "" {
		fields = []string{configured}
	}
	for _, f := range fields {
		if s, ok := navigate(item, f).(string); ok && isWebURL(s) {
			return s
		}
	}
	return ""
}

func isWebURL(s string) bool {
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		!strings.Contains(s, "/_apis/")
}

// adoRepoWebBase maps an ADO git-repository API base
// ("{org}/{project}/_apis/git/repositories/{repo}") to the repo's web page
// ("{org}/{project}/_git/{repo}"). Works for both dev.azure.com and
// on-premise collections, since only the path segment is swapped.
func adoRepoWebBase(repoAPIBase string) string {
	return strings.Replace(repoAPIBase, "/_apis/git/repositories/", "/_git/", 1)
}

// adoFileWebURL builds the web link to file p in the repo an ADO items-list
// URL points at, pinned to the same version (branch/tag/commit) the items
// were listed from, if the URL names one.
func adoFileWebURL(itemsURL, p string) string {
	repoBase, ok := AdoItemsAPIBase(itemsURL)
	if !ok || p == "" {
		return ""
	}
	link := adoRepoWebBase(repoBase) + "?path=" + url.QueryEscape(p)
	if v := adoWebVersion(itemsURL); v != "" {
		link += "&version=" + url.QueryEscape(v)
	}
	return link
}

// adoWebVersion translates an items URL's versionDescriptor query parameters
// into the web UI's version parameter (GB{branch}, GT{tag} or GC{commit}).
func adoWebVersion(itemsURL string) string {
	u, err := url.Parse(itemsURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	version := q.Get("versionDescriptor.version")
	if version == "" {
		return ""
	}
	switch strings.ToLower(q.Get("versionDescriptor.versionType")) {
	case "tag":
		return "GT" + version
	case "commit":
		return "GC" + version
	default:
		return "GB" + version
	}
}

// adoCommitWebURL builds the web link to commitID in the repo an ADO
// commits-list URL points at.
func adoCommitWebURL(commitsURL, commitID string) string {
	repoBase, ok := AdoRepoAPIBase(commitsURL)
	if !ok || commitID == "" {
		return ""
	}
	return adoRepoWebBase(repoBase) + "/commit/" + commitID
}

// adoWorkItemWebURL builds the web link to work item id in project.
func adoWorkItemWebURL(org, project string, id int) string {
	if org == "" || project == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/_workitems/edit/%d", strings.TrimRight(org, "/"), url.PathEscape(project), id)
}
