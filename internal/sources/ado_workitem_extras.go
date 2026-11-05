// Azure DevOps work item links and discussion. The workitemsbatch response
// ($expand=all) carries each item's relations; they become a "Links"
// section so the agent can follow a parent, child, related item, pull
// request, commit or build by its id. The discussion isn't part of the
// item's fields (System.History holds only the latest revision's comment),
// so every comment is fetched from the comments API for items that have
// any, and embedded in order.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// adoWorkItemRelation is one entry of a work item's relations.
type adoWorkItemRelation struct {
	Rel        string         `json:"rel"`
	URL        string         `json:"url"`
	Attributes map[string]any `json:"attributes"`
}

// adoWorkItemComment is one comment of a work item's discussion.
type adoWorkItemComment struct {
	ID        int    `json:"id"`
	Text      string `json:"text"`
	CreatedBy struct {
		DisplayName string `json:"displayName"`
		UniqueName  string `json:"uniqueName"`
	} `json:"createdBy"`
	CreatedDate  string `json:"createdDate"`
	ModifiedDate string `json:"modifiedDate"`
}

var workItemURLID = regexp.MustCompile(`(?i)/_apis/wit/workItems/(\d+)$`)

// workItemLinks renders relations as "Kind: target" lines, grouped by kind.
// Work item targets are written as #id (look them up with the id filter),
// builds as build <id> (the build_id filter of search_pipeline_run), pull
// requests as !<id> and commits by their full SHA.
func workItemLinks(rels []adoWorkItemRelation) []string {
	var lines []string
	for _, r := range rels {
		name, _ := r.Attributes["name"].(string)
		var line string
		switch {
		case workItemURLID.MatchString(r.URL):
			if name == "" {
				name = r.Rel
			}
			line = name + ": #" + workItemURLID.FindStringSubmatch(r.URL)[1]
		case strings.EqualFold(r.Rel, "ArtifactLink"):
			line = artifactLink(name, r.URL)
		case strings.EqualFold(r.Rel, "Hyperlink"):
			line = "Hyperlink: " + r.URL
		case strings.EqualFold(r.Rel, "AttachedFile"):
			if name == "" {
				name = "file"
			}
			line = "Attachment: " + name + " (" + r.URL + ")"
		default:
			if name == "" {
				name = r.Rel
			}
			line = name + ": " + r.URL
		}
		if c, _ := r.Attributes["comment"].(string); strings.TrimSpace(c) != "" {
			line += " — " + strings.TrimSpace(c)
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

// artifactLink renders a vstfs:/// artifact link (pull request, commit,
// build, branch) by kind and id.
func artifactLink(name, raw string) string {
	rest, ok := strings.CutPrefix(raw, "vstfs:///")
	if !ok {
		return orDefault(name, "Artifact") + ": " + raw
	}
	parts := strings.SplitN(rest, "/", 3) // tool, artifact type, id
	if len(parts) != 3 {
		return orDefault(name, "Artifact") + ": " + raw
	}
	id, err := url.PathUnescape(parts[2])
	if err != nil {
		id = parts[2]
	}
	segs := strings.Split(id, "/") // project/repo/value for Git artifacts
	last := segs[len(segs)-1]
	switch strings.ToLower(parts[0] + "/" + parts[1]) {
	case "git/pullrequestid":
		return orDefault(name, "Pull Request") + ": !" + last
	case "git/commit":
		return orDefault(name, "Commit") + ": " + last
	case "build/build":
		return orDefault(name, "Build") + ": build " + last
	case "git/ref":
		// Branch refs are "GB<name>"; tags "GT<name>".
		ref := last
		if len(segs) > 2 {
			ref = strings.Join(segs[2:], "/")
		}
		switch {
		case strings.HasPrefix(ref, "GB"):
			return orDefault(name, "Branch") + ": " + ref[2:]
		case strings.HasPrefix(ref, "GT"):
			return orDefault(name, "Tag") + ": " + ref[2:]
		}
		return orDefault(name, "Ref") + ": " + ref
	}
	return orDefault(name, parts[0]+" "+parts[1]) + ": " + id
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// workItemCommentCount is the item's System.CommentCount.
func workItemCommentCount(item workItemBatchItem) int {
	switch v := item.Fields["System.CommentCount"].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// commentsAPIVersion is the comments API's version: it is a preview API, so
// the configured version gets a -preview suffix unless it already has one.
func commentsAPIVersion(apiVersion string) string {
	if strings.Contains(apiVersion, "preview") {
		return apiVersion
	}
	return apiVersion + "-preview"
}

// fetchWorkItemDiscussions fetches every comment of each item that has any,
// in parallel. Any failure fails the whole fetch, so a sync never embeds a
// discussion with comments missing.
func (a *APISource) fetchWorkItemDiscussions(ctx context.Context, client *http.Client, org, apiVersion string, items []workItemBatchItem, progress ProgressCallback) (map[int][]adoWorkItemComment, error) {
	var todo []workItemBatchItem
	for _, it := range items {
		if workItemCommentCount(it) > 0 {
			todo = append(todo, it)
		}
	}
	out := make(map[int][]adoWorkItemComment, len(todo))
	if len(todo) == 0 {
		return out, nil
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		done     int
		sem      = make(chan struct{}, diffFetchConcurrency)
	)
	for _, it := range todo {
		if workCtx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it workItemBatchItem) {
			defer wg.Done()
			defer func() { <-sem }()
			project := fieldString(it.Fields, "System.TeamProject")
			if project == "" {
				project = a.src.GetConfig("AdoProject")
			}
			comments, err := a.workItemComments(workCtx, client, org, project, apiVersion, it.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("work item %d: fetching discussion: %w", it.ID, err)
				}
				cancel()
				return
			}
			out[it.ID] = comments
			done++
			if progress != nil {
				progress(models.SyncProgress{Phase: "fetching", Current: done, Total: len(todo),
					Message: fmt.Sprintf("Fetching work item discussions %d/%d", done, len(todo))})
			}
		}(it)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// workItemComments pages through one work item's comments, oldest first.
func (a *APISource) workItemComments(ctx context.Context, client *http.Client, org, project, apiVersion string, id int) ([]adoWorkItemComment, error) {
	base := fmt.Sprintf("%s/%s/_apis/wit/workItems/%d/comments?api-version=%s&$top=200&order=asc",
		org, url.PathEscape(project), id, url.QueryEscape(commentsAPIVersion(apiVersion)))
	var all []adoWorkItemComment
	token := ""
	for {
		u := base
		if token != "" {
			u += "&continuationToken=" + url.QueryEscape(token)
		}
		data, err := a.getRaw(ctx, client, u)
		if err != nil {
			return nil, err
		}
		var page struct {
			Comments          []adoWorkItemComment `json:"comments"`
			ContinuationToken string               `json:"continuationToken"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("comments response is not valid JSON: %w", err)
		}
		all = append(all, page.Comments...)
		if page.ContinuationToken == "" || page.ContinuationToken == token {
			break
		}
		token = page.ContinuationToken
	}
	// order=asc is honoured by current servers; sort anyway so older ones
	// (newest first) read the same.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].CreatedDate != all[j].CreatedDate {
			return all[i].CreatedDate < all[j].CreatedDate
		}
		return all[i].ID < all[j].ID
	})
	return all, nil
}

// discussionText renders comments as "author (date): text" entries.
func discussionText(comments []adoWorkItemComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Discussion (%d comment", len(comments))
	if len(comments) != 1 {
		b.WriteString("s")
	}
	b.WriteString("):\n")
	for _, c := range comments {
		who := strings.TrimSpace(c.CreatedBy.DisplayName + " " + angle(c.CreatedBy.UniqueName))
		fmt.Fprintf(&b, "- %s (%s", orDefault(who, "unknown"), c.CreatedDate)
		if c.ModifiedDate != "" && c.ModifiedDate != c.CreatedDate {
			fmt.Fprintf(&b, ", edited %s", c.ModifiedDate)
		}
		fmt.Fprintf(&b, "): %s\n", strings.TrimSpace(stripHTML(c.Text)))
	}
	return strings.TrimRight(b.String(), "\n")
}
