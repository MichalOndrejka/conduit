package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// eventReader reads the data events of one /events stream.
type eventReader struct {
	t      *testing.T
	cancel context.CancelFunc
	lines  chan string
}

func openEvents(t *testing.T, h *harness) *eventReader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.url("/events"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	r := &eventReader{t: t, cancel: cancel, lines: make(chan string, 64)}
	go func() {
		defer close(r.lines)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				r.lines <- data
			}
		}
	}()
	t.Cleanup(cancel)
	return r
}

// next returns the next event's statuses; ok is false once the stream ended.
func (r *eventReader) next() (list []sourceStatus, ok bool) {
	r.t.Helper()
	select {
	case data, open := <-r.lines:
		if !open {
			return nil, false
		}
		if err := json.Unmarshal([]byte(data), &list); err != nil {
			r.t.Fatalf("event is not a status list: %v (%s)", err, data)
		}
		return list, true
	case <-time.After(5 * time.Second):
		r.t.Fatal("no event within 5s")
		return nil, false
	}
}

// waitFor reads events until cond holds for one of them.
func (r *eventReader) waitFor(what string, cond func([]sourceStatus) bool) []sourceStatus {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, ok := r.next()
		if !ok {
			r.t.Fatalf("stream ended before %s", what)
		}
		if cond(list) {
			return list
		}
	}
	r.t.Fatalf("no event with %s", what)
	return nil
}

func TestEventsStreamSendsSnapshotThenProgress(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", Type: "documentation", SyncStatus: "completed"})
	h.qd.mu.Lock()
	h.qd.count["conduit_documentation"] = 4
	h.qd.mu.Unlock()

	ev := openEvents(t, h)
	first, _ := ev.next()
	if len(first) != 1 || first[0].ID != "s1" || first[0].SyncStatus != "completed" {
		t.Fatalf("first event = %+v, want s1/completed", first)
	}

	h.server.sync.Progress().Set("s1", models.SyncProgress{Phase: "indexing", Current: 3, Total: 8})
	ev.waitFor("indexing 3/8", func(list []sourceStatus) bool {
		p := list[0].Progress
		return p != nil && p.Phase == "indexing" && p.Current == 3 && p.Total == 8
	})

	h.server.sync.Control().Pause("s1")
	ev.waitFor("paused", func(list []sourceStatus) bool { return list[0].Paused })

	h.server.sync.Progress().Clear("s1")
	h.server.sync.Control().Resume("s1")
	list := ev.waitFor("progress cleared and resumed", func(list []sourceStatus) bool {
		return list[0].Progress == nil && !list[0].Paused
	})
	if list[0].VectorCount == nil || *list[0].VectorCount != 4 {
		t.Errorf("vector_count = %v, want 4", list[0].VectorCount)
	}
}

func TestEventsStreamSendsSourceChangesWithLastSynced(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", Type: "documentation", SyncStatus: "syncing"})
	ev := openEvents(t, h)
	ev.next()

	synced := time.Date(2026, 11, 9, 14, 2, 0, 0, time.UTC)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", Type: "documentation", SyncStatus: "completed", LastSyncedAt: &synced})
	ev.waitFor("completed with last_synced_at", func(list []sourceStatus) bool {
		return len(list) == 1 && list[0].SyncStatus == "completed" &&
			list[0].LastSyncedAt != nil && list[0].LastSyncedAt.Equal(synced)
	})

	if err := h.sources.Delete("s1"); err != nil {
		t.Fatal(err)
	}
	ev.waitFor("the source gone", func(list []sourceStatus) bool { return len(list) == 0 })
}

func TestCloseEventsEndsOpenStreams(t *testing.T) {
	h := newHarness(t)
	ev := openEvents(t, h)
	ev.next()

	h.server.CloseEvents()
	select {
	case _, open := <-ev.lines:
		if open {
			// A last event raced the close; the stream must still end.
			for range ev.lines {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream still open 5s after CloseEvents")
	}
}

func TestVectorCountIsCachedUntilItsSourceChanges(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", Type: "documentation"})
	setCount := func(n int) {
		h.qd.mu.Lock()
		defer h.qd.mu.Unlock()
		h.qd.count["conduit_documentation"] = n
	}
	countCalls := func() int {
		h.qd.mu.Lock()
		defer h.qd.mu.Unlock()
		return h.qd.countCalls
	}
	statusCount := func() int {
		t.Helper()
		var out []sourceStatus
		if err := json.Unmarshal([]byte(bodyString(t, h.get("/status"))), &out); err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || out[0].VectorCount == nil {
			t.Fatalf("status = %+v, want s1 with a vector count", out)
		}
		return *out[0].VectorCount
	}

	setCount(7)
	if got := statusCount(); got != 7 {
		t.Fatalf("vector_count = %d, want 7", got)
	}
	calls := countCalls()

	setCount(9) // changed behind Conduit's back: the cache doesn't know
	for range 3 {
		if got := statusCount(); got != 7 {
			t.Fatalf("vector_count = %d, want the cached 7", got)
		}
	}
	if got := countCalls(); got != calls {
		t.Errorf("Qdrant counted %d more times; repeated status reads must use the cache", got-calls)
	}

	// A sync ends by saving its source: that drops the cached count.
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", Type: "documentation", SyncStatus: "completed"})
	if got := statusCount(); got != 9 {
		t.Errorf("vector_count after the source changed = %d, want 9", got)
	}
}

// A source change drops the counts of its collection only: a sync can
// change other sources' points there (recreating the collection for a new
// embedding size), but never in another collection.
func TestSourceChangeDropsTheCountsOfItsCollectionOnly(t *testing.T) {
	h := newHarness(t)
	srcs := []models.SourceDefinition{
		{ID: "doc-a", Name: "A", Type: "documentation"},
		{ID: "doc-b", Name: "B", Type: "documentation"},
		{ID: "code", Name: "C", Type: models.SourceCodeRepo},
	}
	for _, src := range srcs {
		mustSave(t, h, src)
	}
	h.server.counts.lookup(srcs, statusWait)

	mustSave(t, h, models.SourceDefinition{ID: "doc-b", Name: "B", Type: "documentation", SyncStatus: "completed"})
	h.server.counts.mu.Lock()
	defer h.server.counts.mu.Unlock()
	for id, want := range map[string]bool{"doc-a": false, "doc-b": false, "code": true} {
		if _, cached := h.server.counts.known[id]; cached != want {
			t.Errorf("after saving doc-b: %s cached = %v, want %v", id, cached, want)
		}
	}
}

func TestStatusSendsNullCountWhenQdrantIsDown(t *testing.T) {
	h := newHarnessNoHealthWait(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", Type: "documentation"})

	body := bodyString(t, h.get("/status"))
	if !strings.Contains(body, `"vector_count":null`) {
		t.Errorf("status = %s, want vector_count null (unknown), not 0 (which shows 'No data embedded')", body)
	}
}
