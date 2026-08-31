// Azure DevOps work-item fetching for APISource. Unlike the other ADO
// presets (commits, repo items), work items can't be listed with a single
// GET — Azure DevOps' Work Item Tracking API is query-then-batch: a WIQL
// query returns matching IDs only, and a separate call fetches each item's
// actual fields. This lets the Work Items source be configured by picking
// which work item types to embed (Bug, Feature, User Story, Task, …)
// instead of hand-crafting a URL with a fixed list of IDs.
package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

const workItemBatchSize = 200 // Azure DevOps' workitemsbatch hard limit

// adoWiqlMaxTop is Azure DevOps' own hard limit on the WIQL $top parameter —
// requesting more than this errors out server-side.
const adoWiqlMaxTop = 19999

// defaultWorkItemFields are fetched for every work item regardless of type —
// covering the fields common process templates (Agile, Scrum, CMMI, Basic)
// use for a work item's title, description and test-case steps.
var defaultWorkItemFields = []string{
	"System.Id", "System.Title", "System.WorkItemType", "System.State",
	"System.AreaPath", "System.Tags", "System.AssignedTo", "System.Description",
	"Microsoft.VSTS.Common.AcceptanceCriteria",
	"Microsoft.VSTS.TCM.ReproSteps", "Microsoft.VSTS.TCM.Steps",
}

// parseWorkItemTypes splits a comma-separated work item type list (e.g.
// "Bug,Task,User Story") into trimmed, non-empty type names.
func parseWorkItemTypes(raw string) []string {
	var types []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			types = append(types, t)
		}
	}
	return types
}

// parseAreaPaths splits a comma-separated area path list (e.g.
// "MyProject\\Team A,MyProject\\Team B") into trimmed, non-empty paths.
func parseAreaPaths(raw string) []string {
	var paths []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// wiqlEscape escapes a value for embedding in a single-quoted WIQL literal.
func wiqlEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

type wiqlResponse struct {
	WorkItems []struct {
		ID int `json:"id"`
	} `json:"workItems"`
}

type workItemBatchItem struct {
	ID     int            `json:"id"`
	Fields map[string]any `json:"fields"`
}

type workItemBatchResponse struct {
	Value []workItemBatchItem `json:"value"`
}

// fetchAdoWorkItems queries Azure DevOps for work items — optionally
// filtered to the configured WorkItemTypes ("" means every type) — via WIQL,
// then fetches each one's fields in batches. Used instead of the generic
// single-URL fetch for Work Items sources on the Azure DevOps preset tab.
func (a *APISource) fetchAdoWorkItems(ctx context.Context, client *http.Client, top int, progress ProgressCallback) ([]models.SourceDocument, error) {
	cfg := a.src
	org := strings.TrimRight(cfg.GetConfig("AdoOrg"), "/")
	project := cfg.GetConfig("AdoProject")
	apiVersion := cfg.GetConfig("AdoApiVersion")
	if apiVersion == "" {
		apiVersion = "7.1"
	}
	if org == "" || project == "" {
		return nil, fmt.Errorf("work item sources require an Azure DevOps organization and project")
	}

	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Querying work items"})
	}
	types := parseWorkItemTypes(cfg.GetConfig("WorkItemTypes"))
	areas := parseAreaPaths(cfg.GetConfig("AreaPaths"))
	ids, err := a.wiqlWorkItemIDs(ctx, client, org, project, apiVersion, types, areas, top)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	items, err := a.workItemsBatch(ctx, client, org, apiVersion, ids, progress)
	if err != nil {
		return nil, err
	}
	return a.workItemsToDocuments(items), nil
}

// wiqlWorkItemIDs runs a WIQL query scoped to project (and, if types/areas
// are non-empty, to those work item types and/or area paths) and returns
// matching work item IDs. Area paths use WIQL's UNDER operator, which also
// matches every sub-area beneath the given path — the same scoping ADO's own
// team boards use.
func (a *APISource) wiqlWorkItemIDs(ctx context.Context, client *http.Client, org, project, apiVersion string, types, areas []string, top int) ([]int, error) {
	query := fmt.Sprintf("SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = '%s'", wiqlEscape(project))
	if len(types) > 0 {
		quoted := make([]string, len(types))
		for i, t := range types {
			quoted[i] = "'" + wiqlEscape(t) + "'"
		}
		query += fmt.Sprintf(" AND [System.WorkItemType] IN (%s)", strings.Join(quoted, ", "))
	}
	if len(areas) > 0 {
		clauses := make([]string, len(areas))
		for i, p := range areas {
			clauses[i] = fmt.Sprintf("[System.AreaPath] UNDER '%s'", wiqlEscape(p))
		}
		query += fmt.Sprintf(" AND (%s)", strings.Join(clauses, " OR "))
	}
	query += " ORDER BY [System.ChangedDate] DESC"

	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	// Azure DevOps' WIQL endpoint rejects $top values above its own hard
	// limit — clamp rather than pass the unlimited sentinel straight through.
	queryTop := top
	if queryTop > adoWiqlMaxTop {
		queryTop = adoWiqlMaxTop
	}
	wiqlURL := fmt.Sprintf("%s/%s/_apis/wit/wiql?api-version=%s&$top=%d", org, url.PathEscape(project), apiVersion, queryTop)
	data, err := a.postJSON(ctx, client, wiqlURL, body)
	if err != nil {
		return nil, err
	}
	var wiql wiqlResponse
	if err := json.Unmarshal(data, &wiql); err != nil {
		return nil, fmt.Errorf("WIQL response is not valid JSON: %w", err)
	}
	ids := make([]int, len(wiql.WorkItems))
	for i, wi := range wiql.WorkItems {
		ids[i] = wi.ID
	}
	if len(ids) > top {
		ids = ids[:top]
	}
	return ids, nil
}

// workItemsBatch fetches full field data for ids in chunks of at most
// workItemBatchSize — Azure DevOps' workitemsbatch endpoint rejects larger
// requests outright.
func (a *APISource) workItemsBatch(ctx context.Context, client *http.Client, org, apiVersion string, ids []int, progress ProgressCallback) ([]workItemBatchItem, error) {
	batchURL := fmt.Sprintf("%s/_apis/wit/workitemsbatch?api-version=%s", org, apiVersion)
	var all []workItemBatchItem
	for i := 0; i < len(ids); i += workItemBatchSize {
		end := i + workItemBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[i:end]
		if progress != nil {
			progress(models.SyncProgress{
				Phase: "fetching", Current: i, Total: len(ids),
				Message: fmt.Sprintf("Fetching work items %d-%d/%d", i+1, end, len(ids)),
			})
		}
		reqBody, err := json.Marshal(map[string]any{"ids": chunk, "fields": defaultWorkItemFields})
		if err != nil {
			return nil, err
		}
		data, err := a.postJSON(ctx, client, batchURL, reqBody)
		if err != nil {
			return nil, err
		}
		var batch workItemBatchResponse
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("workitemsbatch response is not valid JSON: %w", err)
		}
		all = append(all, batch.Value...)
	}
	return all, nil
}

// workItemsToDocuments converts fetched work item fields into documents,
// applying ContentFields (if configured) to select which fields are
// embedded — otherwise every fetched field is included.
func (a *APISource) workItemsToDocuments(items []workItemBatchItem) []models.SourceDocument {
	var contentFields []string
	for _, f := range strings.Split(a.src.GetConfig("ContentFields"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			contentFields = append(contentFields, f)
		}
	}

	docs := make([]models.SourceDocument, 0, len(items))
	for _, item := range items {
		title, _ := item.Fields["System.Title"].(string)
		if title == "" {
			title = fmt.Sprintf("Work Item %d", item.ID)
		}
		wiType, _ := item.Fields["System.WorkItemType"].(string)

		var parts []string
		fieldNames := contentFields
		if fieldNames == nil {
			fieldNames = make([]string, 0, len(item.Fields))
			for k := range item.Fields {
				if k != "System.Title" {
					fieldNames = append(fieldNames, k)
				}
			}
			sort.Strings(fieldNames)
		}
		for _, f := range fieldNames {
			if v, ok := item.Fields[f]; ok && v != nil {
				parts = append(parts, fmt.Sprintf("%s: %v", f, v))
			}
		}

		docs = append(docs, models.SourceDocument{
			ID:   fmt.Sprintf("%s_capi_%d", a.src.ID, item.ID),
			Text: strings.TrimSpace(title + "\n" + strings.Join(parts, "\n")),
			Tags: map[string]string{
				"source_id":   a.src.ID,
				"source_name": a.src.Name,
			},
			Properties: map[string]string{
				"title": title,
				"type":  wiType,
				"area":  fieldString(item.Fields, "System.AreaPath"),
			},
		})
	}
	return docs
}

// postJSON performs an authenticated POST with a JSON body and returns the
// raw response, reusing the source's existing auth/header/TLS configuration.
func (a *APISource) postJSON(ctx context.Context, client *http.Client, reqURL string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	a.applyAuth(req)
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
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, rag.Truncate(string(data), 300))
	}
	return data, nil
}
