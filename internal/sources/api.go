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
//	ItemsPath      dot-path to the items array in the response (e.g. "value")
//	IdField        item field used for stable document IDs (default: position)
//	TitleField     item field used as the document title (default "title")
//	ContentFields  comma-separated fields to index (default: all fields)
//	NextUrlPath    dot-path to a full next-page URL in the response (pagination)
//	Top            max items to fetch across all pages (default 500)
//	VerifySSL      "false" to skip TLS verification (self-hosted instances)
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
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

const (
	defaultTop = 500
	maxPages   = 50 // hard cap so a cyclic NextUrlPath cannot loop forever
)

type APISource struct {
	src     *models.SourceDefinition
	secrets secrets.Reader
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

	client := a.httpClient()
	var items []any
	pageURL := url
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
		pageItems := asList(navigate(data, cfg.GetConfig("ItemsPath")))
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

	return a.itemsToDocuments(items, url), nil
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
	}
	idField := cfg.GetConfig("IdField")
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
