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
//	NextUrlPath    dot-path to a full next-page URL in the response (pagination)
//	Top            max items to fetch across all pages (default 500)
//	VerifySSL      "false" to skip TLS verification (self-hosted instances)
//	PathFilter     comma-separated wildcards (e.g. "*.cs,*.ts") narrowing which
//	               repo files get their content fetched — commit-history/code/
//	               test-code/documentation sources only
//
//	For sources of type commit-history only, each item is automatically enriched
//	with its real code diff, fetched from Azure DevOps' Git REST API — not
//	user-configurable, and never applied to any other source type. Requires
//	Url to be an ADO ".../_apis/git/repositories/{repo}/commits" endpoint;
//	IdField defaults to "commitId" if unset.
//	MaxFilesPerCommit  cap on changed files diffed per commit (default 20)
//	MaxDiffChars       cap on total diff text per commit (default 20000)
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
//	Sources of type requirements are dual-mode, chosen by the RequirementsMode
//	config field: "files" (default) treats it like a Documentation source —
//	real file content fetched from a repo, best-effort if Url isn't an ADO
//	repo-items endpoint; "workitems" treats it like a workitem source — a
//	WIQL query + batch fetch, with the same WorkItemTypes/AreaPaths fields.
package sources

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
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
	defaultTop = 500
	maxPages   = 50 // hard cap so a cyclic NextUrlPath cannot loop forever

	diffFetchConcurrency = 8 // bounded parallelism for per-commit diff enrichment
)

type APISource struct {
	src     *models.SourceDefinition
	secrets secrets.Reader
}

// isWorkItemQuery reports whether cfg should be fetched via the WIQL
// query-then-batch path rather than a single URL GET: workitem sources
// always are; requirements sources are when explicitly set to "workitems"
// mode (they default to "files", the Documentation-style file-content path).
func isWorkItemQuery(cfg *models.SourceDefinition) bool {
	switch cfg.Type {
	case models.SourceWorkItemQuery:
		return true
	case models.SourceRequirements:
		return cfg.GetConfig("RequirementsMode") == "workitems"
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
	case models.SourceRequirements:
		return !isWorkItemQuery(cfg)
	default:
		return false
	}
}

// isFileContentBestEffort reports whether cfg's file-content enrichment may
// silently fall back to plain metadata when Url isn't an ADO repo-items
// endpoint (e.g. a wiki), rather than failing the sync outright.
func isFileContentBestEffort(cfg *models.SourceDefinition) bool {
	return cfg.Type == models.SourceDocumentation || cfg.Type == models.SourceRequirements
}

func (a *APISource) FetchDocuments(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	cfg := a.src
	url := cfg.GetConfig("Url")
	if url == "" {
		return nil, fmt.Errorf("API source requires a Url")
	}

	top := defaultTop
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
	var items []any
	pageURL := fetchURL
	for page := 0; page < maxPages && pageURL != "" && len(items) < top; page++ {
		if progress != nil {
			progress(models.SyncProgress{
				Phase: "fetching", Current: len(items), Total: top,
				Message: fmt.Sprintf("Fetching page %d", page+1),
			})
		}
		data, err := a.fetchPage(ctx, client, pageURL)
		if err != nil {
			return nil, err
		}
		pageItems := resolveItems(data, itemsPath)
		items = append(items, pageItems...)
		pageURL = ""
		if nextPath := cfg.GetConfig("NextUrlPath"); nextPath != "" {
			if next, ok := navigate(data, nextPath).(string); ok {
				pageURL = next
			}
		}
	}
	if len(items) > top {
		items = items[:top]
	}

	// File-content enrichment is automatic for code/test-code sources, and
	// best-effort for documentation sources (which may point at a wiki
	// instead of a repo) — never a user-configurable option, and never
	// applied to any other source type.
	if fileContentType {
		if repoBase, ok := AdoItemsAPIBase(url); ok {
			items = filterGitItems(items, parsePathFilter(cfg.GetConfig("PathFilter")))
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

	docs := a.itemsToDocuments(items, url)
	// Diff enrichment is automatic for commit-history sources only — never a
	// user-configurable option, and never applied to any other source type
	// (it previously ran for whatever source had FetchDiffs=true in its
	// config, regardless of type).
	if cfg.Type == models.SourceGitCommits {
		if err := a.enrichWithDiffs(ctx, client, url, items, docs, progress); err != nil {
			return nil, err
		}
	}
	return docs, nil
}

// diffOutcome is one commit's enrichment result, computed off the main
// goroutine and applied to its document afterwards.
type diffOutcome struct {
	text         string
	filesChanged int
	err          error
}

// enrichWithDiffs appends each commit's real code diff (fetched from Azure
// DevOps) to its document text. items and docs must be the same length and
// order, as produced by itemsToDocuments. Diffs are fetched with bounded
// concurrency since each commit needs several sequential HTTP calls; a
// single commit's diff failing (rate limit, deleted commit, …) is recorded
// as a note on that document rather than failing the whole sync — the other
// commits' documents are still worth keeping.
func (a *APISource) enrichWithDiffs(ctx context.Context, client *http.Client, commitsURL string, items []any, docs []models.SourceDocument, progress ProgressCallback) error {
	cfg := a.src
	repoBase, ok := AdoRepoAPIBase(commitsURL)
	if !ok {
		return fmt.Errorf("commit-history sources require an Azure DevOps commits API Url (got %q)", commitsURL)
	}
	idField := cfg.GetConfig("IdField")
	if idField == "" {
		idField = "commitId" // Azure DevOps' commit list items always key their id this way
	}
	maxFiles := configInt(cfg, "MaxFilesPerCommit", defaultMaxFilesPerCommit)
	maxChars := configInt(cfg, "MaxDiffChars", defaultMaxDiffChars)

	n := len(items)
	if len(docs) < n {
		n = len(docs)
	}
	outcomes := make([]diffOutcome, n)

	var wg sync.WaitGroup
	var progressMu sync.Mutex
	completed := 0
	sem := make(chan struct{}, diffFetchConcurrency)

	for i := 0; i < n; i++ {
		commitID := fieldString(items[i], idField)
		if commitID == "" {
			continue // nothing to diff without a commit id; leave the zero-value outcome
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, commitID string) {
			defer wg.Done()
			defer func() { <-sem }()
			text, filesChanged, err := a.fetchCommitDiff(ctx, client, repoBase, commitID, maxFiles, maxChars)
			outcomes[i] = diffOutcome{text: text, filesChanged: filesChanged, err: err}

			progressMu.Lock()
			completed++
			if progress != nil {
				progress(models.SyncProgress{
					Phase: "fetching", Current: completed, Total: n,
					Message: fmt.Sprintf("Fetching diff %d/%d", completed, n),
				})
			}
			progressMu.Unlock()
		}(i, commitID)
	}
	wg.Wait()

	for i, out := range outcomes {
		switch {
		case out.err != nil:
			docs[i].Text = strings.TrimSpace(docs[i].Text + fmt.Sprintf("\n\n(diff fetch failed: %v)", out.err))
			docs[i].Properties["files_changed"] = "0"
		case out.text != "":
			docs[i].Text = strings.TrimSpace(docs[i].Text + "\n\n" + out.text)
			docs[i].Properties["files_changed"] = strconv.Itoa(out.filesChanged)
		default:
			docs[i].Properties["files_changed"] = strconv.Itoa(out.filesChanged)
		}
	}
	return nil
}

func configInt(cfg *models.SourceDefinition, key string, def int) int {
	if v := cfg.GetConfig(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
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

		docs = append(docs, models.SourceDocument{
			ID:   docID,
			Text: strings.TrimSpace(title + "\n" + strings.Join(parts, "\n")),
			Tags: map[string]string{
				"source_id":   a.src.ID,
				"source_name": a.src.Name,
			},
			Properties: map[string]string{
				"title": title,
				"url":   url,
			},
		})
	}
	return docs
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
func resolveItems(data any, itemsPath string) []any {
	if itemsPath != "" {
		return asList(navigate(data, itemsPath))
	}
	if list, ok := data.([]any); ok {
		return list
	}
	if m, ok := data.(map[string]any); ok {
		if v, ok := m["value"].([]any); ok {
			return v
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
