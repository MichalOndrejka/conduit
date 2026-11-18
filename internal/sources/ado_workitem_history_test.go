package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// historyServer fakes the Azure DevOps endpoints a work item sync with
// history reads: WIQL (the source's ids), workitemsbatch, the revisions feed
// (pages keyed by the continuation token they're asked for) and per-item
// updates.
type historyServer struct {
	t       *testing.T
	ids     []int
	feed    map[string]map[string]any // continuationToken asked → response
	updates map[int][]map[string]any

	mu          sync.Mutex
	feedTokens  []string
	feedQueries []string
	updatesFor  []int
}

func (h *historyServer) start() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			var items []map[string]any
			for _, id := range h.ids {
				items = append(items, map[string]any{"id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": items})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			var body struct {
				IDs []int `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var out []map[string]any
			for _, id := range body.IDs {
				out = append(out, map[string]any{"id": id, "fields": map[string]any{"System.Title": "Item", "System.WorkItemType": "Bug"}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": out})
		case r.URL.Path == "/proj/_apis/wit/reporting/workitemrevisions":
			token := r.URL.Query().Get("continuationToken")
			h.feedTokens = append(h.feedTokens, token)
			h.feedQueries = append(h.feedQueries, r.URL.RawQuery)
			resp, ok := h.feed[token]
			if !ok {
				http.Error(w, "unexpected token "+token, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
		case strings.HasPrefix(r.URL.Path, "/proj/_apis/wit/workItems/") && strings.HasSuffix(r.URL.Path, "/updates"):
			id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/proj/_apis/wit/workItems/"), "/updates"))
			h.updatesFor = append(h.updatesFor, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"value": h.updates[id]})
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	h.t.Cleanup(srv.Close)
	return srv
}

func rev(id, n int, fields map[string]any) map[string]any {
	f := map[string]any{
		"System.Id": id, "System.Rev": n, "System.Watermark": 1000 + n,
		"System.TeamProject": "proj", "System.WorkItemType": "Bug", "System.Title": "Login fails",
		"System.ChangedBy":   map[string]any{"displayName": "Ann Lee", "uniqueName": "ann@corp"},
		"System.ChangedDate": "2026-11-1" + string(rune('0'+n)) + "T10:00:00.5Z",
	}
	for k, v := range fields {
		if v == nil {
			delete(f, k)
		} else {
			f[k] = v
		}
	}
	return map[string]any{"id": id, "rev": n, "fields": f}
}

func historyDocs(docs []models.SourceDocument) map[string]models.SourceDocument {
	out := map[string]models.SourceDocument{}
	for _, d := range docs {
		if d.Properties["record"] == "change" {
			out[d.ID] = d
		}
	}
	return out
}

func TestWorkItemHistoryFirstSyncReadsWholeFeed(t *testing.T) {
	h := &historyServer{t: t, ids: []int{27}, feed: map[string]map[string]any{
		"": {"values": []any{
			rev(27, 1, map[string]any{"System.State": "New"}),
			rev(99, 1, nil), // another area: not this source's
		}, "continuationToken": "t1", "isLastBatch": false},
		"t1": {"values": []any{
			rev(27, 2, map[string]any{"System.State": "Active", "System.History": "<p>Repro on Safari</p>",
				"System.AssignedTo": map[string]any{"displayName": "Bo Chan", "uniqueName": "bo@corp"}}),
			rev(27, 3, map[string]any{"System.State": "Active", "System.Watermark": 2000,
				"System.AssignedTo": map[string]any{"displayName": "Bo Chan", "uniqueName": "bo@corp"}}), // bookkeeping only
		}, "continuationToken": "t2", "isLastBatch": true},
	}}
	srv := h.start()

	s := &APISource{src: workitemAdoSrc(srv.URL, map[string]string{"WorkItemTypes": "Bug"})}
	s.SetSyncState(nil, 0)
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(h.feedTokens, []string{"", "t1"}) {
		t.Errorf("feed read with tokens %q, want the whole feed", h.feedTokens)
	}
	if !strings.Contains(h.feedQueries[0], "types=Bug") || !strings.Contains(h.feedQueries[0], "%24expand=fields") {
		t.Errorf("feed query = %s, want the source's types and long text fields", h.feedQueries[0])
	}
	changes := historyDocs(docs)
	if len(changes) != 2 {
		t.Fatalf("got %d change records %v, want revisions 1 and 2 (3 changed only bookkeeping)", len(changes), changes)
	}
	created := changes["src-1_capi_27_rev_1"]
	if !strings.Contains(created.Text, "#27 Login fails — revision 1 by Ann Lee on 2026-11-11 10:00 UTC") ||
		!strings.Contains(created.Text, "Created Bug #27 with:") || !strings.Contains(created.Text, "- System.State: New") {
		t.Errorf("created record:\n%s", created.Text)
	}
	if strings.Contains(created.Text, "Watermark") || strings.Contains(created.Text, "System.Rev") {
		t.Errorf("bookkeeping fields in the record:\n%s", created.Text)
	}
	r2 := changes["src-1_capi_27_rev_2"]
	for _, want := range []string{"Changed:", "- System.State: New → Active", "- System.AssignedTo: (empty) → Bo Chan <bo@corp>",
		"Comment:\nRepro on Safari"} {
		if !strings.Contains(r2.Text, want) {
			t.Errorf("revision 2 record lacks %q:\n%s", want, r2.Text)
		}
	}
	f := r2.Filters
	for field, want := range map[string][]string{
		models.FilterRecord:       {"change"},
		models.FilterID:           {"27"},
		models.FilterChangedBy:    {"ann lee", "ann@corp"},
		models.FilterChangedField: {"system.assignedto", "assignedto", "system.state", "state", "system.history", "comment"},
	} {
		if got := f.Fields[field]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
	if f.Date.IsZero() || r2.Properties["web_url"] == "" || r2.Properties["changed_by"] != "Ann Lee" {
		t.Errorf("date %v, props %v", f.Date, r2.Properties)
	}

	res := s.SyncResult()
	if res == nil || res.KeepHistory {
		t.Fatalf("result = %+v, want a history rebuild", res)
	}
	var st historyState
	if err := json.Unmarshal(res.State, &st); err != nil {
		t.Fatal(err)
	}
	if st.Token != "t2" || st.Items[27] == nil || st.Items[27].Rev != 3 || st.Items[99] != nil {
		t.Errorf("state = %+v", st)
	}
}

func TestWorkItemHistoryNextSyncReadsOnlyNewRevisions(t *testing.T) {
	long := strings.Repeat("Steps to reproduce. ", 30)
	prior := historyState{Version: historyStateVersion, Token: "t2", Items: map[int]*historySnapshot{
		27: {Rev: 3, Fields: map[string]historyValue{
			"System.State":        {V: "Active", Rev: 2},
			"System.Title":        {V: "Login fails", Rev: 1},
			"System.TeamProject":  {V: "proj", Rev: 1},
			"System.WorkItemType": {V: "Bug", Rev: 1},
			"System.Description":  historyValueOf(long, 2),
		}},
		30: {Rev: 1, Fields: map[string]historyValue{"System.Title": {V: "Gone", Rev: 1}}},
	}}
	h := &historyServer{t: t, ids: []int{27, 28}, feed: map[string]map[string]any{
		"t2": {"values": []any{
			rev(27, 4, map[string]any{"System.State": "Resolved", "System.Description": long + "Updated."}),
			rev(28, 5, map[string]any{"System.State": "Active"}), // joined the source: earlier revisions were passed
		}, "continuationToken": "t3", "isLastBatch": true},
	}, updates: map[int][]map[string]any{
		28: {
			{"rev": 1, "fields": map[string]any{"System.Title": map[string]any{"newValue": "Moved in"},
				"System.State": map[string]any{"newValue": "New"}, "System.WorkItemType": map[string]any{"newValue": "Bug"},
				"System.ChangedBy": map[string]any{"newValue": map[string]any{"displayName": "Cy", "uniqueName": "cy@corp"}}}},
			{"rev": 2}, // a link added: no fields
			{"rev": 3, "fields": map[string]any{"System.State": map[string]any{"oldValue": "New", "newValue": "Active"}}},
		},
	}}
	srv := h.start()
	s := &APISource{src: workitemAdoSrc(srv.URL, nil)}
	prior.Scope = s.historyScope()
	state, _ := json.Marshal(prior)
	s.SetSyncState(state, 5)

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.feedTokens, []string{"t2"}) {
		t.Errorf("feed read with tokens %q, want only from t2", h.feedTokens)
	}
	if !reflect.DeepEqual(h.updatesFor, []int{28}) {
		t.Errorf("updates read for %v, want only the item that joined (28)", h.updatesFor)
	}
	changes := historyDocs(docs)
	r4 := changes["src-1_capi_27_rev_4"]
	if !strings.Contains(r4.Text, "- System.State: Active → Resolved") ||
		!strings.Contains(r4.Text, "- System.Description: (previous value: see revision 2) →\n"+strings.TrimSpace(long)+" Updated.") {
		t.Errorf("revision 4 record:\n%s", r4.Text)
	}
	if strings.Contains(r4.Text, "Login fails →") {
		t.Errorf("unchanged title reported as changed:\n%s", r4.Text)
	}
	if _, ok := changes["src-1_capi_28_rev_1"]; !ok {
		t.Errorf("item 28's history from its updates is missing: %v", keys(changes))
	}
	if r3 := changes["src-1_capi_28_rev_3"]; !strings.Contains(r3.Text, "System.State: New → Active") {
		t.Errorf("item 28 revision 3:\n%s", r3.Text)
	}
	if _, ok := changes["src-1_capi_28_rev_5"]; ok {
		t.Error("feed revision of item 28 used although its history was read from its updates")
	}

	res := s.SyncResult()
	if res == nil || !res.KeepHistory || !reflect.DeepEqual(res.RemoveHistoryIDs, []string{"30"}) {
		t.Fatalf("result = %+v, want history kept and item 30's removed", res)
	}
	var st historyState
	_ = json.Unmarshal(res.State, &st)
	if st.Token != "t3" || st.Items[27].Rev != 4 || st.Items[28] == nil || st.Items[30] != nil {
		t.Errorf("state = %+v", st)
	}
}

func TestWorkItemHistoryStartsOverWhenStateIsUnusable(t *testing.T) {
	s := &APISource{src: workitemAdoSrc("https://ado.example/org", nil)}
	good := historyState{Version: historyStateVersion, Scope: s.historyScope(), Token: "t",
		Items: map[int]*historySnapshot{1: {Rev: 4, Docs: 3}, 2: {Rev: 1, Docs: 1}}}
	for name, tc := range map[string]struct {
		mutate  func(*historyState)
		indexed int
	}{
		"other project":        {func(st *historyState) { st.Scope = "x|other" }, 9},
		"older format":         {func(st *historyState) { st.Version = 0 }, 9},
		"records deleted":      {func(*historyState) {}, 0},
		"some records deleted": {func(*historyState) {}, 3},
	} {
		st := good
		tc.mutate(&st)
		data, _ := json.Marshal(st)
		s.SetSyncState(data, tc.indexed)
		if s.history.prior != nil {
			t.Errorf("%s: kept the prior state, want to start over", name)
		}
	}
	data, _ := json.Marshal(good)
	s.SetSyncState(data, 5) // a long record takes more than one vector
	if s.history.prior == nil || s.history.prior.Token != "t" {
		t.Error("a usable state wasn't kept")
	}
}

// The feed is filtered to the source's types, so an item that was another
// type for a while has revisions missing from it: its whole history is read
// from its updates again, and its records replace the ones indexed.
func TestWorkItemHistoryGapReadsWholeHistory(t *testing.T) {
	prior := historyState{Version: historyStateVersion, Token: "t5", Items: map[int]*historySnapshot{
		42: {Rev: 5, Docs: 5, Fields: map[string]historyValue{"System.State": {V: "Active", Rev: 2}}},
	}}
	h := &historyServer{t: t, ids: []int{42}, feed: map[string]map[string]any{
		"t5": {"values": []any{rev(42, 8, map[string]any{"System.State": "Active"})}, "continuationToken": "t8", "isLastBatch": true},
	}, updates: map[int][]map[string]any{42: {
		{"rev": 1, "fields": map[string]any{"System.Title": map[string]any{"newValue": "Crash"}, "System.State": map[string]any{"newValue": "Active"}}},
		{"rev": 6, "fields": map[string]any{"System.WorkItemType": map[string]any{"newValue": "Task"}}},
		{"rev": 7, "fields": map[string]any{"System.State": map[string]any{"newValue": "Blocked"}}},
		{"rev": 8, "fields": map[string]any{"System.WorkItemType": map[string]any{"newValue": "Bug"}, "System.State": map[string]any{"newValue": "Active"}}},
		{"rev": 9}, // a link: a revision without field changes
	}}}
	srv := h.start()
	s := &APISource{src: workitemAdoSrc(srv.URL, map[string]string{"WorkItemTypes": "Bug"})}
	prior.Scope = s.historyScope()
	state, _ := json.Marshal(prior)
	s.SetSyncState(state, 5)

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.updatesFor, []int{42}) {
		t.Fatalf("updates read for %v, want item 42 after its gap", h.updatesFor)
	}
	changes := historyDocs(docs)
	if r7, ok := changes["src-1_capi_42_rev_7"]; !ok || !strings.Contains(r7.Text, "System.State: Active → Blocked") {
		t.Errorf("revision 7, missing from the feed, isn't recorded: %v", keys(changes))
	}
	res := s.SyncResult()
	if !reflect.DeepEqual(res.RemoveHistoryIDs, []string{"42"}) {
		t.Errorf("remove = %v, want item 42's old records replaced", res.RemoveHistoryIDs)
	}
	var st historyState
	_ = json.Unmarshal(res.State, &st)
	if st.Items[42].Rev != 9 || st.Items[42].Docs != len(changes) {
		t.Errorf("item 42 state = %+v, want revision 9 and %d records", st.Items[42], len(changes))
	}
}

// An empty field is no value: dropping it later is not a change.
func TestWorkItemHistoryEmptyFieldIsNotAChange(t *testing.T) {
	s := &APISource{src: workitemAdoSrc("https://ado.example/org", nil)}
	_, snap := s.changeDocument("", adoRevision{ID: 1, Rev: 1, Fields: map[string]any{"System.Title": "T", "Priority": ""}}, nil)
	doc, _ := s.changeDocument("", adoRevision{ID: 1, Rev: 2, Fields: map[string]any{"System.Title": "T"}}, snap)
	if doc != nil {
		t.Errorf("a dropped empty field made a record:\n%s", doc.Text)
	}
	_, snap = s.changeDocument("", adoRevision{ID: 1, Rev: 3, Fields: map[string]any{"System.Title": "T", "Priority": "2"}}, snap)
	doc, _ = s.changeDocument("", adoRevision{ID: 1, Rev: 4, Fields: map[string]any{"System.Title": "T", "Priority": ""}}, snap)
	if doc == nil || !strings.Contains(doc.Text, "Priority: 2 → (empty)") {
		t.Errorf("clearing a field isn't recorded: %v", doc)
	}
}

func TestNonWorkItemSourcesKeepNoHistory(t *testing.T) {
	s := &APISource{src: &models.SourceDefinition{Type: models.SourceDocumentation, Config: map[string]string{"Platform": "ado"}}}
	s.SetSyncState(nil, 0)
	if s.history != nil || s.SyncResult() != nil {
		t.Error("a documentation source took on work item history")
	}
}

func keys(m map[string]models.SourceDocument) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
