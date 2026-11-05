package mcptools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// sortField is one order_by a collection's list mode offers: a numeric
// payload key the sources compute at sync time.
type sortField struct {
	name   string
	key    string // payload key
	schema string // Qdrant index type: integer or float
	desc   string
	// gap names the documents this order leaves out (they lack the key)
	// and how to list them; shown unless the request already filters on unless.
	gap, unless string
}

func sortOn(name, desc string) sortField {
	return sortField{name: name, key: models.SortKey(name), schema: "float", desc: desc}
}

// collectionSorts lists each collection's sort keys, the default (newest
// first) one first. Collections without any list in storage order.
var collectionSorts = map[string][]sortField{
	models.CollectionCommits: {
		{name: "authored", key: models.FilterDateKey, schema: "integer", desc: "Author date"},
	},
	models.CollectionPipelineRuns: {
		{name: "finished", key: models.SortKey("finished"), schema: "float", desc: "Finish time",
			gap: `Runs still queued or running at the last sync have no finish time, so this order leaves them out — ` +
				`list them with filters.status "inProgress" (Azure DevOps) or "in_progress"/"queued" (GitHub) and order_by "queued".`,
			unless: models.FilterStatus},
		sortOn("queued", "Queue time"),
		sortOn("started", "Start time"),
		sortOn("duration", "Run duration in seconds"),
		sortOn("attempts", "Number of attempts"),
		sortOn("last_failed", "test_history: when the test last failed"),
		sortOn("fail_count", "test_history: runs in which the test failed"),
		sortOn("flaky_count", "test_history: runs in which the test passed only on retry"),
	},
	models.CollectionPullRequests: {
		{name: "created", key: models.FilterDateKey, schema: "integer", desc: "Creation date"},
	},
}

// listDefaults are filters a collection's list mode applies when the
// request doesn't set that field, with the note that says so: a pipeline
// listing is of runs unless asked for steps, tests, deployments or test
// history.
var listDefaults = map[string]struct{ field, value, note string }{
	models.CollectionPipelineRuns: {models.FilterType, "run",
		`Listing run documents only (the default) — pass filters.type "step", "test", "deployment" or "test_history" for those.`},
}

// ListIndexes are the sort keys of every collection, so startup can create
// their payload indexes before the first listing needs them.
func ListIndexes() map[string][]rag.OrderBy {
	out := map[string][]rag.OrderBy{}
	for coll, sorts := range collectionSorts {
		for _, s := range sorts {
			out[coll] = append(out[coll], rag.OrderBy{Key: s.key, Schema: s.schema})
		}
	}
	return out
}

// listRequest is request.mode == "list": documents (not chunks) matching the
// filters, sorted by a stored field — "latest", "last failed", "slowest" —
// answered from the index alone.
type listRequest struct {
	Filters []rag.FieldCondition
	Order   rag.OrderBy
	OrderBy string
	Page    int
	Notes   []string // defaults applied, documents the order leaves out
}

func (q listRequest) resolve(ctx context.Context, search *rag.SearchService, collection string) (map[string]any, error) {
	results, hasMore, err := search.List(ctx, collection, q.Filters, q.Order, q.Page)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"results": results, "page": q.Page, "has_more": hasMore}
	if len(q.Notes) > 0 {
		payload["list_notes"] = q.Notes
	}
	if q.OrderBy != "" {
		dir := "ascending"
		if q.Order.Desc {
			dir = "descending"
		}
		payload["order"] = q.OrderBy + " " + dir
	}
	if len(results) == 0 {
		payload["results"] = []any{}
		switch {
		case q.Page > 1:
			payload["note"] = fmt.Sprintf("No further documents beyond page %d — this was the last page.", q.Page-1)
		case q.OrderBy != "":
			payload["note"] = "Nothing matched these filters, or no document carries " + q.OrderBy +
				" yet: sources synced before Conduit stored it need a re-sync (sync_source trigger)."
		default:
			payload["note"] = "Nothing matched these filters — the source may not be synced yet."
		}
	}
	return payload, nil
}

func parseListRequest(raw map[string]any, fields []filterField, sorts []sortField, me []string, collection string) (chunkQuery, error) {
	page := 1
	if p, ok := raw["page"].(float64); ok && p >= 1 {
		page = int(p)
	}
	given := map[string]any{}
	if m, ok := raw["filters"].(map[string]any); ok {
		for k, v := range m {
			given[k] = v
		}
	} else if raw["filters"] != nil {
		return nil, fmt.Errorf("request.filters must be an object")
	}
	var notes []string
	if d, ok := listDefaults[collection]; ok {
		if _, set := given[d.field]; !set {
			given[d.field] = d.value
			notes = append(notes, d.note)
		}
	}
	filters, err := parseFiltersFor(given, fields, me)
	if err != nil {
		return nil, err
	}
	q := listRequest{Filters: filters, Page: page, Notes: notes}
	name, _ := raw["order_by"].(string)
	if name == "" && len(sorts) > 0 {
		name = sorts[0].name
	}
	if name != "" {
		var sf *sortField
		for i := range sorts {
			if sorts[i].name == name {
				sf = &sorts[i]
			}
		}
		if sf == nil {
			return nil, fmt.Errorf("unknown order_by %q — this tool sorts by: %s", name, sortNames(sorts))
		}
		q.OrderBy = name
		q.Order = rag.OrderBy{Key: sf.key, Schema: sf.schema, Desc: true}
		if sf.gap != "" {
			if _, set := given[sf.unless]; !set {
				q.Notes = append(q.Notes, sf.gap)
			}
		}
	}
	switch dir, _ := raw["direction"].(string); strings.ToLower(dir) {
	case "", "desc":
	case "asc":
		q.Order.Desc = false
	default:
		return nil, fmt.Errorf(`request.direction must be "desc" or "asc", got %q`, dir)
	}
	return q, nil
}

func sortNames(sorts []sortField) string {
	if len(sorts) == 0 {
		return "(nothing — results come in storage order)"
	}
	names := make([]string, len(sorts))
	for i, s := range sorts {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}

// listModeSchema is the "list" variant of a search tool's request.
func listModeSchema(fields []filterField, sorts []sortField) map[string]any {
	props := map[string]any{
		"mode":    map[string]any{"const": "list"},
		"filters": filtersSchema(fields),
		"page": map[string]any{"type": "number", "description": fmt.Sprintf("Page of %d documents, starting at 1 (default 1)",
			rag.ListPageSize)},
	}
	if len(sorts) > 0 {
		names := make([]string, len(sorts))
		var parts []string
		for i, s := range sorts {
			names[i] = s.name
			parts = append(parts, s.name+": "+s.desc)
		}
		props["order_by"] = map[string]any{"type": "string", "enum": names,
			"description": "Sort key (default " + sorts[0].name + "). " + strings.Join(parts, "; ")}
		props["direction"] = map[string]any{"type": "string", "enum": []string{"desc", "asc"},
			"description": "desc (default) puts the newest / largest first"}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"mode"},
	}
}

// addAsOf stamps a response with when each source in its results last
// synced: the index is a cache, so every answer says how fresh it is.
func addAsOf(payload map[string]any, search *rag.SearchService) {
	results, ok := payload["results"].([]models.SearchResult)
	if !ok || len(results) == 0 {
		return
	}
	payload["as_of"] = search.AsOf(results)
}

// listModeNote describes the list mode and the cache's freshness in a
// search tool's description.
func listModeNote(sorts []sortField) string {
	order := "in storage order"
	if len(sorts) > 0 {
		order = "sorted by order_by (default " + sorts[0].name + ", newest first)"
	}
	return ` For "latest", "last", "newest", "most recent" or "every X matching Y" questions don't use a search — ` +
		`pass request={"mode":"list","filters":{...},"page":1}: whole documents (one result each, its first chunk; ` +
		`walk the rest with retrieve_chunk), ` + order + `, 20 per page. ` +
		`Conduit is a cache of the source systems: every response carries as_of, when each source in the results ` +
		`last synced. If the question is about right now and as_of is older than that matters, call sync_source ` +
		`(mode trigger, then status until completed) and ask again.`
}
