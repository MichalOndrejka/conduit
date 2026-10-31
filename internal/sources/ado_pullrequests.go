// Azure DevOps pull requests (see pullrequests.go). Diffs are assembled
// client-side from blobs, as for commits (ado_diff.go): ADO lists an
// iteration's changed files but has no patch endpoint.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// adoPRChangesPageSize is the most changes ADO returns per iteration-changes
// call.
const adoPRChangesPageSize = 2000

type adoPRClient struct {
	api     *APISource // auth, headers and TLS settings of the matched source
	client  *http.Client
	org     string // organization / collection URL
	project string
	repo    string
	version string
}

func newAdoPRClient(src *models.SourceDefinition, store secrets.Reader, org, project, repo string) *adoPRClient {
	api := &APISource{src: src, secrets: store}
	return &adoPRClient{
		api: api, client: api.httpClient(),
		org: strings.TrimRight(org, "/"), project: project, repo: repo,
		version: adoAPIVersion(src),
	}
}

// adoAPIVersion is the source's API version: the form's AdoApiVersion, else
// its Url's api-version, else 7.1.
func adoAPIVersion(src *models.SourceDefinition) string {
	if v := strings.TrimSpace(src.GetConfig("AdoApiVersion")); v != "" {
		return v
	}
	if u, err := url.Parse(src.GetConfig("Url")); err == nil {
		if v := u.Query().Get("api-version"); v != "" {
			return v
		}
	}
	return "7.1"
}

func (c *adoPRClient) projectBase() string {
	return c.org + "/" + url.PathEscape(c.project)
}

func (c *adoPRClient) repoAPI() string {
	return c.projectBase() + "/_apis/git/repositories/" + url.PathEscape(c.repo)
}

func (c *adoPRClient) prAPI(id int) string {
	return fmt.Sprintf("%s/pullRequests/%d", c.repoAPI(), id)
}

func (c *adoPRClient) WebURL(id int) string {
	return fmt.Sprintf("%s/_git/%s/pullrequest/%d", c.projectBase(), url.PathEscape(c.repo), id)
}

// query encodes q with the client's api-version, leaving ADO's "$" query
// parameter prefixes ($top, $skip, …) unescaped.
func (c *adoPRClient) query(q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	if q.Get("api-version") == "" {
		q.Set("api-version", c.version)
	}
	return "?" + strings.ReplaceAll(q.Encode(), "%24", "$")
}

func (c *adoPRClient) getJSON(ctx context.Context, reqURL string, v any) (http.Header, error) {
	data, header, err := c.api.getRawWithHeader(ctx, c.client, reqURL)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return nil, fmt.Errorf("response from %s is not valid JSON: %w", reqURL, err)
	}
	return header, nil
}

type adoIdentityRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
}

func (r adoIdentityRef) String() string {
	switch {
	case r.UniqueName == "" || r.UniqueName == r.DisplayName:
		return r.DisplayName
	case r.DisplayName == "":
		return r.UniqueName
	default:
		return r.DisplayName + " <" + r.UniqueName + ">"
	}
}

type adoCommitRef struct {
	CommitID string `json:"commitId"`
}

type adoPullRequest struct {
	PullRequestID int            `json:"pullRequestId"`
	Title         string         `json:"title"`
	Description   string         `json:"description"`
	Status        string         `json:"status"`
	CreatedBy     adoIdentityRef `json:"createdBy"`
	CreationDate  string         `json:"creationDate"`
	SourceRefName string         `json:"sourceRefName"`
	TargetRefName string         `json:"targetRefName"`
	IsDraft       bool           `json:"isDraft"`
	MergeStatus   string         `json:"mergeStatus"`
	Reviewers     []struct {
		adoIdentityRef
		Vote       int  `json:"vote"`
		IsRequired bool `json:"isRequired"`
	} `json:"reviewers"`
	LastMergeSourceCommit adoCommitRef `json:"lastMergeSourceCommit"`
	LastMergeTargetCommit adoCommitRef `json:"lastMergeTargetCommit"`
	Repository            struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	} `json:"repository"`
}

func (c *adoPRClient) summary(pr adoPullRequest) PRSummary {
	return PRSummary{
		ID: pr.PullRequestID, Title: pr.Title, Status: pr.Status, Author: pr.CreatedBy.String(),
		SourceBranch: strings.TrimPrefix(pr.SourceRefName, "refs/heads/"),
		TargetBranch: strings.TrimPrefix(pr.TargetRefName, "refs/heads/"),
		IsDraft:      pr.IsDraft, CreatedAt: pr.CreationDate, WebURL: c.WebURL(pr.PullRequestID),
	}
}

func (c *adoPRClient) List(ctx context.Context, f PRListFilter) ([]PRSummary, bool, error) {
	status := f.Status
	if status == "" {
		status = PRStatusActive
	}
	q := url.Values{"searchCriteria.status": {status}}
	for _, who := range []struct{ key, value string }{
		{"searchCriteria.creatorId", f.Author}, {"searchCriteria.reviewerId", f.Reviewer},
	} {
		if who.value == "" {
			continue
		}
		id, err := c.identityID(ctx, who.value)
		if err != nil {
			return nil, false, err
		}
		q.Set(who.key, id)
	}
	if b := f.TargetBranch; b != "" {
		if !strings.HasPrefix(b, "refs/") {
			b = "refs/heads/" + b
		}
		q.Set("searchCriteria.targetRefName", b)
	}
	page := max(f.Page, 1)
	// One extra item tells whether another page follows.
	q.Set("$top", strconv.Itoa(PRPageSize+1))
	q.Set("$skip", strconv.Itoa((page-1)*PRPageSize))

	var resp struct {
		Value []adoPullRequest `json:"value"`
	}
	if _, err := c.getJSON(ctx, c.repoAPI()+"/pullrequests"+c.query(q), &resp); err != nil {
		return nil, false, err
	}
	out := []PRSummary{}
	for i, pr := range resp.Value {
		if i == PRPageSize {
			break
		}
		out = append(out, c.summary(pr))
	}
	return out, len(resp.Value) > PRPageSize, nil
}

// identityID resolves "me" (the token's owner) or an email to an Azure
// DevOps identity ID.
func (c *adoPRClient) identityID(ctx context.Context, who string) (string, error) {
	if strings.EqualFold(who, "me") {
		var data struct {
			AuthenticatedUser struct {
				ID string `json:"id"`
			} `json:"authenticatedUser"`
		}
		if _, err := c.getJSON(ctx, c.org+"/_apis/connectionData", &data); err != nil {
			return "", fmt.Errorf("resolving \"me\": %w", err)
		}
		if data.AuthenticatedUser.ID == "" {
			return "", fmt.Errorf("resolving \"me\": Azure DevOps returned no authenticated user")
		}
		return data.AuthenticatedUser.ID, nil
	}
	q := url.Values{"searchFilter": {"General"}, "filterValue": {who}, "queryMembership": {"None"}}
	var data struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if _, err := c.getJSON(ctx, c.identitiesAPI()+c.query(q), &data); err != nil {
		return "", fmt.Errorf("resolving %q: %w", who, err)
	}
	if len(data.Value) == 0 || data.Value[0].ID == "" {
		return "", fmt.Errorf("no Azure DevOps identity matches %q — use the user's email or \"me\"", who)
	}
	return data.Value[0].ID, nil
}

// identitiesAPI is the identities endpoint: Azure DevOps Services keeps it on
// the organization's vssps host, Azure DevOps Server on the collection.
func (c *adoPRClient) identitiesAPI() string {
	u, err := url.Parse(c.org)
	if err == nil {
		host := strings.ToLower(u.Host)
		switch {
		case host == "dev.azure.com":
			org, _, _ := strings.Cut(strings.Trim(u.Path, "/"), "/")
			return u.Scheme + "://vssps.dev.azure.com/" + org + "/_apis/identities"
		case strings.HasSuffix(host, ".visualstudio.com"):
			org := strings.TrimSuffix(host, ".visualstudio.com")
			return u.Scheme + "://" + org + ".vssps.visualstudio.com/_apis/identities"
		}
	}
	return c.org + "/_apis/identities"
}

// adoVotes names Azure DevOps reviewer votes.
var adoVotes = map[int]string{
	10: "approved", 5: "approved_with_suggestions", 0: "no_vote", -5: "waiting_for_author", -10: "rejected",
}

func (c *adoPRClient) Get(ctx context.Context, id int) (*PullRequest, error) {
	var pr adoPullRequest
	if _, err := c.getJSON(ctx, c.prAPI(id)+c.query(nil), &pr); err != nil {
		return nil, err
	}
	out := &PullRequest{
		PRSummary: c.summary(pr), Description: pr.Description, MergeStatus: pr.MergeStatus,
		HeadCommit: pr.LastMergeSourceCommit.CommitID, BaseCommit: pr.LastMergeTargetCommit.CommitID,
		Reviewers: []PRReviewer{}, WorkItems: []PRWorkItem{}, Checks: []PRCheck{},
	}
	for _, r := range pr.Reviewers {
		vote, ok := adoVotes[r.Vote]
		if !ok {
			vote = strconv.Itoa(r.Vote)
		}
		out.Reviewers = append(out.Reviewers, PRReviewer{Name: r.DisplayName, ID: r.UniqueName, Vote: vote, Required: r.IsRequired})
	}

	items, err := c.workItems(ctx, id)
	out.WorkItems = append(out.WorkItems, items...)
	if err != nil {
		out.Notes = append(out.Notes, "linked work items could not be read fully: "+err.Error())
	}
	checks, errs := c.checks(ctx, id, pr.Repository.Project.ID)
	out.Checks = append(out.Checks, checks...)
	for _, err := range errs {
		out.Notes = append(out.Notes, "checks could not be read fully: "+err.Error())
	}
	return out, nil
}

// workItems lists the pull request's linked work items with their title,
// type and state. On a failed detail lookup the bare IDs are still returned.
func (c *adoPRClient) workItems(ctx context.Context, id int) ([]PRWorkItem, error) {
	var refs struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if _, err := c.getJSON(ctx, c.prAPI(id)+"/workitems"+c.query(nil), &refs); err != nil {
		return nil, err
	}
	items := make([]PRWorkItem, 0, len(refs.Value))
	byID := map[string]int{}
	for _, r := range refs.Value {
		byID[r.ID] = len(items)
		n, _ := strconv.Atoi(r.ID)
		items = append(items, PRWorkItem{ID: r.ID, WebURL: adoWorkItemWebURL(c.org, c.project, n)})
	}
	const batch = 200 // ADO's most IDs per workitems call
	for start := 0; start < len(items); start += batch {
		var ids []string
		for _, it := range items[start:min(start+batch, len(items))] {
			ids = append(ids, it.ID)
		}
		q := url.Values{"ids": {strings.Join(ids, ",")}, "fields": {"System.Title,System.State,System.WorkItemType"}}
		var details struct {
			Value []struct {
				ID     int               `json:"id"`
				Fields map[string]string `json:"fields"`
			} `json:"value"`
		}
		if _, err := c.getJSON(ctx, c.org+"/_apis/wit/workitems"+c.query(q), &details); err != nil {
			return items, err
		}
		for _, d := range details.Value {
			if i, ok := byID[strconv.Itoa(d.ID)]; ok {
				items[i].Title = d.Fields["System.Title"]
				items[i].State = d.Fields["System.State"]
				items[i].Type = d.Fields["System.WorkItemType"]
			}
		}
	}
	return items, nil
}

// checks reads the pull request's posted statuses (latest per context) and
// its branch policy evaluations (build validation, required reviewers, …).
func (c *adoPRClient) checks(ctx context.Context, id int, projectID string) ([]PRCheck, []error) {
	var out []PRCheck
	var errs []error

	var statuses struct {
		Value []struct {
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"targetUrl"`
			Context     struct {
				Name  string `json:"name"`
				Genre string `json:"genre"`
			} `json:"context"`
		} `json:"value"`
	}
	if _, err := c.getJSON(ctx, c.prAPI(id)+"/statuses"+c.query(nil), &statuses); err != nil {
		errs = append(errs, err)
	} else {
		latest := map[string]int{} // statuses are listed oldest first
		for _, s := range statuses.Value {
			name := s.Context.Name
			if s.Context.Genre != "" {
				name = s.Context.Genre + "/" + name
			}
			check := PRCheck{Name: name, State: s.State, Description: s.Description, WebURL: s.TargetURL}
			if i, ok := latest[name]; ok {
				out[i] = check
			} else {
				latest[name] = len(out)
				out = append(out, check)
			}
		}
	}

	if projectID != "" {
		version := c.version
		if !strings.Contains(version, "preview") {
			version += "-preview.1"
		}
		q := url.Values{
			"artifactId":  {fmt.Sprintf("vstfs:///CodeReview/CodeReviewId/%s/%d", projectID, id)},
			"api-version": {version},
		}
		var evals struct {
			Value []struct {
				Status        string `json:"status"`
				Configuration struct {
					IsBlocking bool `json:"isBlocking"`
					IsEnabled  bool `json:"isEnabled"`
					Type       struct {
						DisplayName string `json:"displayName"`
					} `json:"type"`
					Settings map[string]any `json:"settings"`
				} `json:"configuration"`
				Context map[string]any `json:"context"`
			} `json:"value"`
		}
		if _, err := c.getJSON(ctx, c.projectBase()+"/_apis/policy/evaluations"+c.query(q), &evals); err != nil {
			errs = append(errs, err)
		} else {
			for _, e := range evals.Value {
				if !e.Configuration.IsEnabled {
					continue
				}
				name := e.Configuration.Type.DisplayName
				if dn, _ := e.Configuration.Settings["displayName"].(string); dn != "" {
					name += ": " + dn
				}
				check := PRCheck{Name: name, State: e.Status, Blocking: e.Configuration.IsBlocking}
				if buildID, ok := e.Context["buildId"].(float64); ok {
					check.WebURL = fmt.Sprintf("%s/_build/results?buildId=%d", c.projectBase(), int(buildID))
				}
				out = append(out, check)
			}
		}
	}
	return out, errs
}

type adoPRIteration struct {
	ID              int            `json:"id"`
	Description     string         `json:"description"`
	Author          adoIdentityRef `json:"author"`
	CreatedDate     string         `json:"createdDate"`
	SourceRefCommit adoCommitRef   `json:"sourceRefCommit"`
	TargetRefCommit adoCommitRef   `json:"targetRefCommit"`
	CommonRefCommit adoCommitRef   `json:"commonRefCommit"`
}

func (c *adoPRClient) iterations(ctx context.Context, id int) ([]adoPRIteration, error) {
	var resp struct {
		Value []adoPRIteration `json:"value"`
	}
	if _, err := c.getJSON(ctx, c.prAPI(id)+"/iterations"+c.query(nil), &resp); err != nil {
		return nil, err
	}
	return resp.Value, nil
}

func (c *adoPRClient) Iterations(ctx context.Context, id int) ([]PRIteration, error) {
	iters, err := c.iterations(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]PRIteration, 0, len(iters))
	for _, it := range iters {
		out = append(out, PRIteration{
			Number: it.ID, HeadCommit: it.SourceRefCommit.CommitID, BaseCommit: it.CommonRefCommit.CommitID,
			Author: it.Author.String(), CreatedAt: it.CreatedDate, Description: it.Description,
		})
	}
	return out, nil
}

func (c *adoPRClient) Changes(ctx context.Context, id int, r PRRange) (PRChanges, error) {
	iters, err := c.iterations(ctx, id)
	if err != nil {
		return PRChanges{}, err
	}
	if r, err = checkRange(r, len(iters)); err != nil {
		return PRChanges{}, err
	}
	to := iters[r.To-1]
	head := to.SourceRefCommit.CommitID
	base := to.CommonRefCommit.CommitID
	if r.From > 0 {
		base = iters[r.From-1].SourceRefCommit.CommitID
	}

	out := PRChanges{Files: []PRChange{}, HeadCommit: head}
	for skip := 0; ; {
		q := url.Values{
			"$top": {strconv.Itoa(adoPRChangesPageSize)}, "$skip": {strconv.Itoa(skip)}, "$compareTo": {strconv.Itoa(r.From)},
		}
		var page struct {
			ChangeEntries []struct {
				adoChange
				OriginalPath string `json:"originalPath"`
			} `json:"changeEntries"`
			NextSkip int `json:"nextSkip"`
		}
		if _, err := c.getJSON(ctx, fmt.Sprintf("%s/iterations/%d/changes%s", c.prAPI(id), r.To, c.query(q)), &page); err != nil {
			return PRChanges{}, err
		}
		for _, e := range page.ChangeEntries {
			if e.Item.Path == "" || strings.EqualFold(e.ChangeType, "none") || e.Item.IsFolder || strings.EqualFold(e.Item.GitObjectType, "tree") {
				continue
			}
			ch := PRChange{
				Path: e.Item.Path, ChangeType: e.ChangeType,
				oldObjectID: e.Item.OriginalObjectID, newObjectID: e.Item.ObjectID,
				baseCommit: base, headCommit: head,
			}
			if orig := firstNonEmpty(e.OriginalPath, e.SourceServerItem); orig != "" && orig != e.Item.Path {
				ch.OriginalPath = orig
			}
			out.Files = append(out.Files, ch)
		}
		if page.NextSkip <= skip || len(page.ChangeEntries) == 0 {
			return out, nil
		}
		skip = page.NextSkip
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *adoPRClient) Diff(ctx context.Context, changes []PRChange, withContent bool) ([]PRFile, error) {
	out := make([]PRFile, len(changes))
	errs := make([]error, len(changes))
	sem := make(chan struct{}, diffFetchConcurrency)
	var wg sync.WaitGroup
	for i, ch := range changes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = c.diffOne(ctx, ch, withContent)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("diffing %s: %w", changes[i].Path, err)
		}
	}
	return out, nil
}

func (c *adoPRClient) diffOne(ctx context.Context, ch PRChange, withContent bool) (PRFile, error) {
	kind := strings.ToLower(ch.ChangeType)
	added, deleted := strings.Contains(kind, "add"), strings.Contains(kind, "delete")
	oldPath := firstNonEmpty(ch.OriginalPath, ch.Path)

	var oldText, newText string
	var err error
	if !added {
		if oldText, err = c.fileText(ctx, ch.oldObjectID, oldPath, ch.baseCommit); err != nil {
			return PRFile{}, err
		}
	}
	if !deleted {
		if newText, err = c.fileText(ctx, ch.newObjectID, ch.Path, ch.headCommit); err != nil {
			return PRFile{}, err
		}
	}
	diff, err := blobDiff(oldPath, ch.Path, oldText, newText, ch.ChangeType)
	if err != nil {
		return PRFile{}, err
	}
	f := PRFile{Path: ch.Path, OriginalPath: ch.OriginalPath, ChangeType: ch.ChangeType, Diff: diff}
	f.Additions, f.Deletions = diffLineCounts(diff)
	if withContent && !deleted && !isBinary(newText) {
		f.Content = &newText
	}
	return f, nil
}

// fileText reads a file version by blob ID, or — when ADO didn't give one —
// by path at commit.
func (c *adoPRClient) fileText(ctx context.Context, objectID, path, commit string) (string, error) {
	if objectID != "" {
		q := url.Values{"$format": {"text"}}
		data, err := c.api.getRaw(ctx, c.client, c.repoAPI()+"/blobs/"+url.PathEscape(objectID)+c.query(q))
		return string(data), err
	}
	q := url.Values{
		"path": {path}, "includeContent": {"true"},
		"versionDescriptor.version": {commit}, "versionDescriptor.versionType": {"commit"},
	}
	var item struct {
		Content string `json:"content"`
	}
	if _, err := c.getJSON(ctx, c.repoAPI()+"/items"+c.query(q), &item); err != nil {
		return "", err
	}
	return item.Content, nil
}

func (c *adoPRClient) Threads(ctx context.Context, id int) ([]PRThread, []string, error) {
	type position struct {
		Line int `json:"line"`
	}
	var resp struct {
		Value []struct {
			ID            int    `json:"id"`
			Status        string `json:"status"`
			IsDeleted     bool   `json:"isDeleted"`
			ThreadContext *struct {
				FilePath       string    `json:"filePath"`
				RightFileStart *position `json:"rightFileStart"`
				LeftFileStart  *position `json:"leftFileStart"`
			} `json:"threadContext"`
			Comments []struct {
				ID              int            `json:"id"`
				ParentCommentID int            `json:"parentCommentId"`
				Author          adoIdentityRef `json:"author"`
				Content         string         `json:"content"`
				PublishedDate   string         `json:"publishedDate"`
				CommentType     string         `json:"commentType"`
				IsDeleted       bool           `json:"isDeleted"`
			} `json:"comments"`
		} `json:"value"`
	}
	if _, err := c.getJSON(ctx, c.prAPI(id)+"/threads"+c.query(nil), &resp); err != nil {
		return nil, nil, err
	}
	out := []PRThread{}
	for _, t := range resp.Value {
		if t.IsDeleted {
			continue
		}
		th := PRThread{ID: strconv.Itoa(t.ID), Kind: "general", Status: t.Status, Comments: []PRComment{}}
		if tc := t.ThreadContext; tc != nil && tc.FilePath != "" {
			th.Kind, th.Path = "file", tc.FilePath
			switch {
			case tc.RightFileStart != nil:
				th.Line = tc.RightFileStart.Line
			case tc.LeftFileStart != nil:
				th.Line = tc.LeftFileStart.Line
			}
		}
		for _, cm := range t.Comments {
			// System comments are ADO's own activity log (votes, pushes,
			// policy updates), not review discussion.
			if cm.IsDeleted || strings.EqualFold(cm.CommentType, "system") {
				continue
			}
			pc := PRComment{ID: strconv.Itoa(cm.ID), Author: cm.Author.String(), Body: cm.Content, CreatedAt: cm.PublishedDate}
			if cm.ParentCommentID > 0 {
				pc.ReplyTo = strconv.Itoa(cm.ParentCommentID)
			}
			th.Comments = append(th.Comments, pc)
		}
		if len(th.Comments) > 0 {
			out = append(out, th)
		}
	}
	return out, nil, nil
}

func (c *adoPRClient) Commits(ctx context.Context, id int) ([]PRCommit, []string, error) {
	out := []PRCommit{}
	for token := ""; ; {
		q := url.Values{"$top": {"100"}}
		if token != "" {
			q.Set("continuationToken", token)
		}
		var page struct {
			Value []struct {
				CommitID         string `json:"commitId"`
				Comment          string `json:"comment"`
				CommentTruncated bool   `json:"commentTruncated"`
				Author           struct {
					Name  string `json:"name"`
					Email string `json:"email"`
					Date  string `json:"date"`
				} `json:"author"`
			} `json:"value"`
		}
		header, err := c.getJSON(ctx, c.prAPI(id)+"/commits"+c.query(q), &page)
		if err != nil {
			return nil, nil, err
		}
		for _, cm := range page.Value {
			msg := cm.Comment
			if cm.CommentTruncated {
				if msg, err = c.api.fetchCommitComment(ctx, c.client, c.repoAPI(), cm.CommitID); err != nil {
					return nil, nil, fmt.Errorf("reading the message of commit %s: %w", cm.CommitID, err)
				}
			}
			author := cm.Author.Name
			if cm.Author.Email != "" {
				author += " <" + cm.Author.Email + ">"
			}
			out = append(out, PRCommit{
				SHA: cm.CommitID, Message: msg, Author: author, Date: cm.Author.Date,
				WebURL: fmt.Sprintf("%s/_git/%s/commit/%s", c.projectBase(), url.PathEscape(c.repo), cm.CommitID),
			})
		}
		token = header.Get("x-ms-continuationtoken")
		if token == "" || len(page.Value) == 0 {
			return out, nil, nil
		}
	}
}
