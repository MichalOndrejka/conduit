// Generic HTTP API source — Go port of app/sources/custom_api.py, extended so
// that real-world systems (Azure DevOps, Jira, GitHub, …) can be expressed as
// configuration instead of provider-specific code:
//
//	Url            endpoint returning JSON
//	HttpMethod     GET (default) | POST
//	Body           raw JSON request body for POST endpoints
//	Headers        extra headers, one "Name: value" per line
//	AuthType       none | bearer | basic | apikey
//	Token          credential name for bearer auth
//	Username       username for basic auth (may be empty — e.g. ADO PATs)
//	Password       credential name for basic auth password
//	ApiKeyHeader   header name for apikey auth (default X-Api-Key)
//	ApiKeyValue    credential name for apikey auth
//	ItemsPath      dot-path to the items array in the response (default "value",
//	               Azure DevOps' own list envelope)
//	IdField        item field used for stable document IDs (default: position)
//	TitleField     item field used as the document title (default "title")
//	ContentFields  comma-separated fields to index (default: all fields)
//	WebUrlField    dot-path to the item's browser (UI) link, stored as the
//	               web_url property (default: the first of _links.html.href,
//	               _links.web.href, remoteUrl, webUrl, html_url, web_url;
//	               ADO repo files and commits fall back to a link built
//	               from Url)
//	NextUrlPath    dot-path to a full next-page URL in the response (pagination)
//	Top            max items to fetch across all pages (default: unlimited —
//	               every item the source has is fetched)
//	VerifySSL      "false" to skip TLS verification (self-hosted instances)
//	PathFilter     comma-separated wildcards (e.g. "*.cs,*.ts") narrowing which
//	               repo files get their content fetched — code/test-code/
//	               documentation sources only
//
//	For sources of type commit-history only, each item is automatically enriched
//	with its full commit message and complete code diff (every changed file,
//	untruncated), fetched from Azure DevOps' Git REST API — not
//	user-configurable, and never applied to any other source type. A commit
//	that can't be fetched after retries fails the sync rather than being
//	indexed partially. Requires Url to be an ADO
//	".../_apis/git/repositories/{repo}/commits" endpoint; IdField defaults to
//	"commitId" if unset. Since ADO's commits endpoint has no next-page link,
//	more than one page (adoCommitsPageSize items) is fetched by paging
//	manually via searchCriteria.$top/$skip until Top is reached or the repo
//	runs out of commits.
//	Branch             branch whose history is listed (default "main") —
//	                   otherwise ADO's commits endpoint walks every branch,
//	                   duplicating shared commits and pulling in stray
//	                   feature-branch work.
//
//	For sources of type code or test-code, each item is automatically enriched
//	with its real file content (in place of the raw JSON metadata), fetched
//	from Azure DevOps' Git REST API. Requires Url to be an ADO
//	".../_apis/git/repositories/{repo}/items" endpoint. Documentation sources
//	get the same treatment on a best-effort basis, falling back to plain
//	metadata if Url isn't an ADO repo-items endpoint (e.g. a wiki).
//
//	For sources of type work-item built on the Azure DevOps preset (Platform
//	config field set to "ado"), documents come from a WIQL query + batch
//	fetch against Azure DevOps' Work Item Tracking API instead of the
//	single-URL fetch above — not user-configurable via Url. WorkItemTypes
//	restricts which work item types (e.g. "Bug,Task,User Story") are queried;
//	left blank, every type in the project is fetched. AreaPaths restricts to
//	one or more team area paths (comma-separated, e.g. "Proj\\Team A") and
//	their sub-areas; left blank, every area in the project is fetched.
//
//	Sources of type requirements or test-case are dual-mode, chosen by the
//	FetchMode config field: "files" (default) treats it like a Documentation
//	source — real file content fetched from a repo, best-effort if Url isn't
//	an ADO repo-items endpoint; "workitems" treats it like a work-item
//	source — a WIQL query + batch fetch, with the same WorkItemTypes/AreaPaths
//	fields.
package sources

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

const (
	// unlimitedTop is the effective item cap when the source has no
	// user-configured Top — every item the source has is fetched, bounded
	// only by maxPages below.
	unlimitedTop = math.MaxInt32
	maxPages     = 50 // hard cap so a cyclic NextUrlPath cannot loop forever

	diffFetchConcurrency = 8 // bounded parallelism for per-commit diff enrichment
)

type APISource struct {
	src     *models.SourceDefinition
	secrets secrets.Reader
}

// isWorkItemQuery reports whether cfg should be fetched via the WIQL
// query-then-batch path rather than a single URL GET: work-item sources
// always are; requirements and test-case sources are when explicitly set to
// "workitems" mode (they default to "files", the Documentation-style
// file-content path).
func isWorkItemQuery(cfg *models.SourceDefinition) bool {
	switch cfg.Type {
	case models.SourceWorkItemQuery:
		return true
	case models.SourceRequirements, models.SourceTestCase:
		return cfg.GetConfig("FetchMode") == "workitems"
	default:
		return false
	}
}

// isFileContentType reports whether cfg's items should be enriched with real
// file content fetched from an ADO repo, in place of raw JSON metadata.
func isFileContentType(cfg *models.SourceDefinition) bool {
	switch cfg.Type {
	case models.SourceCodeRepo, models.SourceTestCodeRepo, models.SourceDocumentation:
		return true
	case models.SourceRequirements, models.SourceTestCase:
		return !isWorkItemQuery(cfg)
	default:
		return false
	}
}

// isFileContentBestEffort reports whether cfg's file-content enrichment may
// silently fall back to plain metadata when Url isn't an ADO repo-items
// endpoint (e.g. a wiki), rather than failing the sync outright.
func isFileContentBestEffort(cfg *models.SourceDefinition) bool {
	switch cfg.Type {
	case models.SourceDocumentation, models.SourceRequirements, models.SourceTestCase:
		return true
	default:
		return false
	}
}

func (a *APISource) FetchDocuments(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	cfg := a.src
	url := cfg.GetConfig("Url")
	if url == "" {
		return nil, fmt.Errorf("API source requires a Url")
	}

	top := unlimitedTop
	if t := cfg.GetConfig("Top"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			top = n
		}
	}

	// Work Items sources on the Azure DevOps preset query-then-batch instead
	// of a single GET, so picking work item types works without the caller
	// hand-crafting a URL with a fixed list of IDs — not applicable to Work
	// Items sources built on the Custom API tab, which keep the generic path.
	if isWorkItemQuery(cfg) && cfg.GetConfig("Platform") == "ado" {
		return a.fetchAdoWorkItems(ctx, a.httpClient(), top, progress)
	}

	fetchURL := url
	fileContentType := isFileContentType(cfg)
	if fileContentType {
		// ADO's items-list endpoint returns only the repo root without this —
		// not user-configurable now that the "Extra query" field is gone.
		fetchURL = withRecursionFull(fetchURL)
	}

	client := a.httpClient()
	itemsPath := cfg.GetConfig("ItemsPath")
	// progressTotal is left at 0 (meaning "unknown") when top is the
	// unlimited default — a real Total of math.MaxInt32 would render as a
	// nonsense "x/2147483647" in the UI.
	progressTotal := top
	if top == unlimitedTop {
		progressTotal = 0
	}
	// Azure DevOps' commits-list endpoint returns no next-page link for
	// NextUrlPath to follow, and caps a response at adoCommitsPageSize items
	// — without manually paging via searchCriteria.$top/$skip, a
	// commit-history source silently stops at that page size regardless of
	// its own Top setting.
	paginateAdoCommits := cfg.Type == models.SourceGitCommits && cfg.GetConfig("NextUrlPath") == ""
	if cfg.Type == models.SourceGitCommits {
		if _, ok := AdoRepoAPIBase(url); !ok {
			return nil, fmt.Errorf("commit-history sources require an Azure DevOps commits API Url (got %q)", url)
		}
	}
	if paginateAdoCommits {
		// Unfiltered, ADO's commits endpoint walks every branch's history,
		// which duplicates commits shared with the default branch and pulls
		// in throwaway feature-branch work — almost never what a "commit
		// history" source wants indexed.
		branch := cfg.GetConfig("Branch")
		if branch == "" {
			branch = defaultCommitsBranch
		}
		fetchURL = withAdoBranchFilter(fetchURL, branch)
	}
	skip := 0
	lastPageFirstID := ""
	var items []any
	pageURL := fetchURL
	// maxPages guards against a cyclic NextUrlPath; ADO commit paging can't
	// cycle (it stops on the first empty page), so it's exempt — capping it
	// would silently drop the oldest commits of a long history.
	for page := 0; (paginateAdoCommits || page < maxPages) && pageURL != "" && len(items) < top; page++ {
		reqURL := pageURL
		if paginateAdoCommits {
			pageSize := adoCommitsPageSize
			if top != unlimitedTop && top-len(items) < pageSize {
				pageSize = top - len(items)
			}
			reqURL = withAdoCommitsPaging(pageURL, pageSize, skip)
		}
		if progress != nil {
			progress(models.SyncProgress{
				Phase: "fetching", Current: len(items), Total: progressTotal,
				Message: fmt.Sprintf("Fetching page %d", page+1),
			})
		}
		data, err := a.fetchPage(ctx, client, reqURL)
		if err != nil {
			return nil, err
		}
		pageItems := resolveItems(data, itemsPath)
		if paginateAdoCommits && page > 0 && len(pageItems) > 0 {
			// Uncapped paging relies on $skip advancing; a server that ignores
			// it would otherwise return the same page forever.
			if first := fieldString(pageItems[0], "commitId"); first != "" && first == lastPageFirstID {
				return nil, fmt.Errorf("commits endpoint returned the same page twice (commit %s) — it is ignoring searchCriteria.$skip", first)
			}
		}
		if len(pageItems) > 0 {
			lastPageFirstID = fieldString(pageItems[0], "commitId")
		}
		items = append(items, pageItems...)
		pageURL = ""
		if paginateAdoCommits {
			if len(pageItems) > 0 {
				skip += len(pageItems)
				pageURL = fetchURL // keep paging; loop guards (Top, maxPages) bound it
			}
		} else if nextPath := cfg.GetConfig("NextUrlPath"); nextPath != "" {
			if next, ok := navigate(data, nextPath).(string); ok {
				pageURL = next
			}
		}
	}
	// File-content enrichment is automatic for code/test-code sources, and
	// best-effort for documentation sources (which may point at a wiki
	// instead of a repo) — never a user-configurable option, and never
	// applied to any other source type.
	if fileContentType {
		if repoBase, ok := AdoItemsAPIBase(url); ok {
			// PathFilter must run before the Top truncation below — otherwise
			// a small Top (e.g. the preview's 5-item cap) can slice off every
			// item that would have matched the filter, since ADO returns the
			// full repo tree in an arbitrary order.
			items = filterGitItems(items, parsePathFilter(cfg.GetConfig("PathFilter")))
			if len(items) > top {
				items = items[:top]
			}
			docs := a.itemsToDocuments(items, url)
			if err := a.enrichWithFileContent(ctx, client, repoBase, items, docs, progress); err != nil {
				return nil, err
			}
			return docs, nil
		}
		if !isFileContentBestEffort(cfg) {
			return nil, fmt.Errorf("%s sources require an Azure DevOps items API Url (…/_apis/git/repositories/{repo}/items)", cfg.Type)
		}
		// Documentation and file-mode Requirements: not an ADO repo-items
		// URL (e.g. a wiki) — fall through to plain metadata handling below.
	}

	if len(items) > top {
		items = items[:top]
	}
	// Commit enrichment is automatic for commit-history sources only — never
	// a user-configurable option, and never applied to any other source type.
	if cfg.Type == models.SourceGitCommits {
		return a.commitDocuments(ctx, client, url, items, progress)
	}
	return a.itemsToDocuments(items, url), nil
}

// commitOutcome is one commit's enrichment result, computed off the main
// goroutine and applied to its document afterwards.
type commitOutcome struct {
	fullComment  string // set only when the listed comment was truncated
	diff         string
	filesChanged int
	paths        []string
}

// commitDocuments turns listed commits into documents, each carrying the
// commit's full message and complete code diff fetched from Azure DevOps.
// Commits are fetched with bounded concurrency since each needs several
// sequential HTTP calls. Any commit that still can't be fetched after retries
// fails the whole sync: indexing a partial result would silently miss
// changes, whereas failing leaves the previous index intact.
func (a *APISource) commitDocuments(ctx context.Context, client *http.Client, commitsURL string, items []any, progress ProgressCallback) ([]models.SourceDocument, error) {
	repoBase, ok := AdoRepoAPIBase(commitsURL)
	if !ok {
		return nil, fmt.Errorf("commit-history sources require an Azure DevOps commits API Url (got %q)", commitsURL)
	}
	idField := a.src.GetConfig("IdField")
	if idField == "" {
		idField = "commitId" // Azure DevOps' commit list items always key their id this way
	}

	n := len(items)
	outcomes := make([]commitOutcome, n)

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		completed int
		sem       = make(chan struct{}, diffFetchConcurrency)
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}

	for i := 0; i < n && workCtx.Err() == nil; i++ {
		commitID := fieldString(items[i], idField)
		if commitID == "" {
			fail(fmt.Errorf("commit item %d has no %q field", i, idField))
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, commitID string) {
			defer wg.Done()
			defer func() { <-sem }()

			var out commitOutcome
			if truncated, _ := navigate(items[i], "commentTruncated").(bool); truncated {
				comment, err := a.fetchCommitComment(workCtx, client, repoBase, commitID)
				if err != nil {
					fail(fmt.Errorf("commit %s: fetching full message: %w", commitID, err))
					return
				}
				out.fullComment = comment
			}
			diff, filesChanged, paths, err := a.fetchCommitDiff(workCtx, client, repoBase, commitID)
			if err != nil {
				fail(fmt.Errorf("commit %s: %w", commitID, err))
				return
			}
			out.diff, out.filesChanged, out.paths = diff, filesChanged, paths
			outcomes[i] = out

			mu.Lock()
			completed++
			if progress != nil {
				progress(models.SyncProgress{
					Phase: "fetching", Current: completed, Total: n,
					Message: fmt.Sprintf("Fetching commit %d/%d", completed, n),
				})
			}
			mu.Unlock()
		}(i, commitID)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	for i, out := range outcomes {
		if m, ok := items[i].(map[string]any); ok && out.fullComment != "" {
			m["comment"] = out.fullComment
			delete(m, "commentTruncated")
		}
	}
	docs := a.itemsToDocuments(items, commitsURL)
	for i, out := range outcomes {
		if out.diff != "" {
			docs[i].Text = strings.TrimSpace(docs[i].Text + "\n\n" + out.diff)
		}
		docs[i].Properties["files_changed"] = strconv.Itoa(out.filesChanged)

		f := &docs[i].Filters
		f.Add(models.FilterPath, out.paths...)
		if author, ok := navigate(items[i], "author").(map[string]any); ok {
			name, _ := author["name"].(string)
			email, _ := author["email"].(string)
			f.Add(models.FilterAuthor, name, email)
			f.Date = parseTime(author["date"])
		}
	}
	return docs, nil
}

func (a *APISource) httpClient() *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if strings.EqualFold(a.src.GetConfig("VerifySSL"), "false") {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-hosted instances with private CAs
		}
	}
	return client
}

func (a *APISource) fetchPage(ctx context.Context, client *http.Client, url string) (any, error) {
	method := strings.ToUpper(a.src.GetConfig("HttpMethod"))
	if method != http.MethodPost {
		method = http.MethodGet
	}
	var body io.Reader
	if method == http.MethodPost {
		if b := a.src.GetConfig("Body"); b != "" {
			body = strings.NewReader(b)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	a.applyAuth(req)
	a.applyExtraHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, rag.Truncate(string(data), 300))
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("response is not valid JSON: %w", err)
	}
	return parsed, nil
}

func (a *APISource) secret(credName string) string {
	if credName == "" || a.secrets == nil {
		return ""
	}
	return a.secrets.GetValue(credName)
}

func (a *APISource) applyAuth(req *http.Request) {
	cfg := a.src
	switch strings.ToLower(cfg.GetConfig("AuthType")) {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+a.secret(cfg.GetConfig("Token")))
	case "basic":
		// Username may legitimately be empty — e.g. Azure DevOps PATs go in
		// the password slot with a blank username.
		req.SetBasicAuth(cfg.GetConfig("Username"), a.secret(cfg.GetConfig("Password")))
	case "apikey":
		header := cfg.GetConfig("ApiKeyHeader")
		if header == "" {
			header = "X-Api-Key"
		}
		req.Header.Set(header, a.secret(cfg.GetConfig("ApiKeyValue")))
	}
}

// applyExtraHeaders parses the Headers config: one "Name: value" per line.
func (a *APISource) applyExtraHeaders(req *http.Request) {
	for _, line := range strings.Split(a.src.GetConfig("Headers"), "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name != "" && value != "" {
			req.Header.Set(name, value)
		}
	}
}

func (a *APISource) itemsToDocuments(items []any, url string) []models.SourceDocument {
	cfg := a.src
	titleField := cfg.GetConfig("TitleField")
	if titleField == "" {
		titleField = "title"
		if isFileContentType(cfg) {
			titleField = "path"
		}
	}
	idField := cfg.GetConfig("IdField")
	switch {
	case idField != "":
		// user-configured — leave as-is
	case cfg.Type == models.SourceGitCommits:
		idField = "commitId" // keep doc IDs stable across syncs, matching enrichWithDiffs' default
	case isFileContentType(cfg):
		idField = "path" // ADO repo-items list items key on path, not id
	}
	var contentFields []string
	for _, f := range strings.Split(cfg.GetConfig("ContentFields"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			contentFields = append(contentFields, f)
		}
	}

	docs := make([]models.SourceDocument, 0, len(items))
	for i, item := range items {
		title := fieldString(item, titleField)
		if title == "" && idField != "" {
			// Fall back to the item's own ID rather than its position in this
			// batch — a positional "Item N" is meaningless (and misleading)
			// once IdField selects an arbitrary item like ADO's `ids=...`.
			if v := fieldString(item, idField); v != "" {
				title = "Item " + v
			}
		}
		if title == "" {
			title = fmt.Sprintf("Item %d", i+1)
		}

		var parts []string
		if len(contentFields) > 0 {
			for _, f := range contentFields {
				if v := fieldString(item, f); v != "" {
					parts = append(parts, f+": "+v)
				}
			}
		} else if m, ok := item.(map[string]any); ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				if k != titleField {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys) // deterministic order (Python dicts preserve insertion; JSON maps don't)
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s: %v", k, m[k]))
			}
		} else {
			parts = append(parts, fmt.Sprintf("%v", item))
		}

		// Stable IDs when IdField is configured; positional otherwise
		// (matching the Python `{source_id}_capi_{i}` scheme).
		docID := fmt.Sprintf("%s_capi_%d", a.src.ID, i)
		if idField != "" {
			if v := fieldString(item, idField); v != "" {
				docID = fmt.Sprintf("%s_capi_%s", a.src.ID, v)
			}
		}

		props := map[string]string{"title": title}
		if webURL := a.webURL(item, url, idField); webURL != "" {
			props["web_url"] = webURL
		}
		filters := a.scopeFilters(url, props)
		if isFileContentType(cfg) {
			p := fieldString(item, "path")
			addFileFilters(&filters, p)
		}
		docs = append(docs, models.SourceDocument{
			Filters: filters,
			ID:   docID,
			Text: strings.TrimSpace(title + "\n" + strings.Join(parts, "\n")),
			Tags: map[string]string{
				"source_id":   a.src.ID,
				"source_name": a.src.Name,
			},
			Properties: props,
		})
	}
	return docs
}

// webURL returns item's browser link: one the item carries itself, else one
// built from the ADO API url it was listed from (repo files and commits,
// whose list responses carry no web link of their own).
func (a *APISource) webURL(item any, url, idField string) string {
	if link := itemWebURL(item, a.src.GetConfig("WebUrlField")); link != "" {
		return link
	}
	switch {
	case isFileContentType(a.src):
		return adoFileWebURL(url, fieldString(item, "path"))
	case a.src.Type == models.SourceGitCommits:
		return adoCommitWebURL(url, fieldString(item, idField))
	}
	return ""
}

// ── JSON helpers (ports of _navigate/_field in custom_api.py) ───────────────

func navigate(data any, path string) any {
	if path == "" {
		return data
	}
	for _, key := range strings.Split(path, ".") {
		m, ok := data.(map[string]any)
		if !ok {
			return nil
		}
		data = m[key]
		if data == nil {
			return nil
		}
	}
	return data
}

// resolveItems extracts the list of items from a page response. With an
// explicit ItemsPath it's a plain navigate+asList. Left blank (now the case
// for every Azure DevOps preset field, since ItemsPath is no longer a visible
// ADO-tab input), it auto-detects: a response that's already a bare array is
// used as-is; a map is checked for ADO's own "value" envelope; anything else
// falls back to treating the whole response as a single item, unchanged from
// the pre-auto-detection behavior.
//
// A map that *has* a "value" key but where it isn't a JSON array (e.g. an
// empty trailing page serialized as "value": null instead of "value": [])
// deliberately resolves to zero items rather than falling through to the
// single-item wrap below — otherwise that fallback turns an empty envelope
// page into one bogus document containing the entire raw response, and a
// pagination loop that only stops on a truly empty page (see
// paginateAdoCommits) would keep "successfully" fetching that same bogus
// page indefinitely.
func resolveItems(data any, itemsPath string) []any {
	if itemsPath != "" {
		return asList(navigate(data, itemsPath))
	}
	if list, ok := data.([]any); ok {
		return list
	}
	if m, ok := data.(map[string]any); ok {
		if v, present := m["value"]; present {
			list, _ := v.([]any)
			return list
		}
	}
	return asList(data)
}

func asList(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	default:
		return []any{t}
	}
}

func fieldString(item any, name string) string {
	m, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m[name]
	if !ok || v == nil {
		return ""
	}
	// Render numbers without the %v float noise where possible.
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
