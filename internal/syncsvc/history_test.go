package syncsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// An Azure DevOps work item source keeps its history between syncs: the next
// sync reads the revisions feed only from where the last successful one left
// off, keeps the history records already indexed, and a failed sync doesn't
// move the feed position on.
func TestWorkItemHistoryIsIncrementalAcrossSyncs(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	rev := func(n int, state string) map[string]any {
		return map[string]any{"id": 27, "rev": n, "fields": map[string]any{
			"System.Title": "Login fails", "System.WorkItemType": "Bug", "System.State": state,
			"System.ChangedBy":   map[string]any{"displayName": "Ann Lee", "uniqueName": "ann@corp"},
			"System.ChangedDate": "2026-11-1" + string(rune('0'+n)) + "T10:00:00Z",
		}}
	}
	feed := map[string]map[string]any{
		"":   {"values": []any{rev(1, "New")}, "continuationToken": "t1", "isLastBatch": true},
		"t1": {"values": []any{rev(2, "Active")}, "continuationToken": "t2", "isLastBatch": true},
		"t2": {"values": []any{}, "continuationToken": "t2", "isLastBatch": true},
	}
	ado := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []any{map[string]any{"id": 27}}})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": 27,
				"fields": map[string]any{"System.Title": "Login fails", "System.WorkItemType": "Bug"}}}})
		case strings.HasSuffix(r.URL.Path, "/reporting/workitemrevisions"):
			token := r.URL.Query().Get("continuationToken")
			mu.Lock()
			tokens = append(tokens, token)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(feed[token])
		default:
			http.NotFound(w, r)
		}
	}))
	defer ado.Close()

	var embedFails atomic.Bool
	svc, st, qd, src := setupPipelineWithEmbed(t, ado.URL, func(w http.ResponseWriter, r *http.Request) {
		if embedFails.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		defaultEmbedHandler(w, r)
	})
	src.Config = map[string]string{"Provider": "api", "Platform": "ado", "AdoOrg": ado.URL, "AdoProject": "proj",
		"Url": ado.URL + "/proj"}
	if err := st.Save(*src); err != nil {
		t.Fatal(err)
	}
	history := func() int {
		n := 0
		qd.mu.Lock()
		defer qd.mu.Unlock()
		for _, p := range qd.collections[models.CollectionWorkItems] {
			if p[models.PropKey("record")] == "change" {
				n++
			}
		}
		return n
	}
	doSync := func() {
		t.Helper()
		svc.Sync(context.Background(), src.ID)
	}

	doSync()
	if got := history(); got != 1 {
		saved, _ := st.Get(src.ID)
		t.Fatalf("history records after the first sync = %d, want 1 (status %s, error %v, points %v)",
			got, saved.SyncStatus, saved.SyncError, qd.collections)
	}
	doSync()
	if got := history(); got != 2 {
		t.Fatalf("history records after the second sync = %d, want revision 1 kept and 2 added", got)
	}
	if got := qd.count(models.CollectionWorkItems); got != 3 {
		t.Errorf("points = %d, want the item and two history records", got)
	}

	embedFails.Store(true)
	doSync() // nothing new, and fails: the position mustn't move
	embedFails.Store(false)
	doSync()

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"", "t1", "t2", "t2"}; strings.Join(tokens, ",") != strings.Join(want, ",") {
		t.Errorf("feed read from %q, want %q", tokens, want)
	}
	if got := history(); got != 2 {
		t.Errorf("history records at the end = %d, want 2", got)
	}
}
