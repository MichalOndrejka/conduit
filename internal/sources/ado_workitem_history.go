// Azure DevOps work item history: who changed what, when. Every revision of
// every work item the source covers becomes one change document (record =
// change) beside the items themselves, so search can answer "who moved this
// to Resolved", "what did Ann change last week" or "when did the estimate
// change".
//
// A revision never changes once made, so history is the one part of a work
// item sync that is incremental: it is read from the reporting revisions feed
// (one paged stream for the whole project), and the feed's continuation token
// is kept between syncs, so a later sync reads only the revisions made since —
// usually a single request. The items, their fields and their discussions can
// change, so those are still re-read on every sync.
package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// historyStateVersion is bumped when the change documents or the state
// change shape, so the next sync rebuilds the history instead of mixing
// formats.
const historyStateVersion = 2

// historyShortValue is the longest field value kept verbatim in the state;
// longer ones (descriptions, repro steps) are kept as a hash, enough to tell
// whether they changed. Their full text is in the change document of the
// revision that set them.
const historyShortValue = 256

// historyIgnoredFields are bookkeeping fields every revision changes (or that
// mirror another field), left out of the change list. System.ChangedBy and
// System.ChangedDate head every change document instead; System.History is
// the comment added with the revision and is shown as such.
var historyIgnoredFields = map[string]bool{
	"System.Id": true, "System.Rev": true, "System.Watermark": true,
	"System.AuthorizedDate": true, "System.AuthorizedAs": true,
	"System.ChangedDate": true, "System.ChangedBy": true,
	"System.RevisedDate": true, "System.PersonId": true,
	"System.CommentCount": true, "System.AreaId": true, "System.IterationId": true,
	"System.NodeName": true, "System.History": true,
}

func historyIgnored(field string) bool {
	return historyIgnoredFields[field] ||
		strings.HasPrefix(field, "System.AreaLevel") || strings.HasPrefix(field, "System.IterationLevel")
}

// historyState is what a work item source keeps between syncs: where the
// revisions feed left off, and each covered item's latest known fields, so
// the first new revision of an item can be compared with the one before it.
type historyState struct {
	Version int                      `json:"version"`
	Scope   string                   `json:"scope"` // organization + project
	Token   string                   `json:"token"` // revisions feed continuation token
	Items   map[int]*historySnapshot `json:"items"`
}

type historySnapshot struct {
	Rev    int                     `json:"rev"`
	Fields map[string]historyValue `json:"fields"`
	// Docs is how many history records the item has, so a sync can tell
	// when some of them have gone from the index.
	Docs int `json:"docs"`
}

// historyValue is one field's value: verbatim when short (V), else its hash
// (H). Rev is the revision that set it.
type historyValue struct {
	V   string `json:"v,omitempty"`
	H   string `json:"h,omitempty"`
	Rev int    `json:"r"`
}

func (v historyValue) same(o historyValue) bool { return v.V == o.V && v.H == o.H }

// historyRun is one sync's history work: the state it started from and,
// once FetchDocuments has run, the state and instructions for indexing.
type historyRun struct {
	prior  *historyState // nil: no usable state, rebuild from the feed's start
	result *SyncResult
}

// SetSyncState implements StatefulSource. Only Azure DevOps work item
// sources keep state; for any other source it is ignored.
func (a *APISource) SetSyncState(state []byte, indexedHistory int) {
	if !isWorkItemQuery(a.src) || a.src.GetConfig("Platform") != "ado" {
		return
	}
	run := &historyRun{}
	var st historyState
	if len(state) > 0 && json.Unmarshal(state, &st) == nil &&
		st.Version == historyStateVersion && st.Scope == a.historyScope() &&
		indexedHistory >= st.records() {
		run.prior = &st
	}
	a.history = run
}

// records is how many history records the state's items have. The index
// holds at least that many vectors of them (a long record is several);
// fewer means some were deleted and carrying on would leave them missing.
func (st *historyState) records() int {
	n := 0
	for _, snap := range st.Items {
		n += snap.Docs
	}
	return n
}

// SyncResult implements StatefulSource.
func (a *APISource) SyncResult() *SyncResult {
	if a.history == nil {
		return nil
	}
	return a.history.result
}

// historyScope identifies the revisions feed the state belongs to. Types,
// area paths and the item limit only decide which items are covered, which
// every sync works out afresh, so changing them doesn't rebuild the history.
func (a *APISource) historyScope() string {
	org := strings.ToLower(strings.TrimRight(strings.TrimSpace(a.src.GetConfig("AdoOrg")), "/"))
	project := strings.ToLower(strings.TrimSpace(a.src.GetConfig("AdoProject")))
	return org + "|" + project
}

// adoRevision is one work item revision: the item's fields as they were
// right after it.
type adoRevision struct {
	ID     int            `json:"id"`
	Rev    int            `json:"rev"`
	Fields map[string]any `json:"fields"`
}

// fetchWorkItemHistory turns the revisions made since the last sync into
// change documents for the items in ids (the source's items this sync).
func (a *APISource) fetchWorkItemHistory(ctx context.Context, client *http.Client, org, project, apiVersion string,
	ids []int, progress ProgressCallback) ([]models.SourceDocument, error) {
	run := a.history
	st := run.prior
	rebuild := st == nil
	if rebuild {
		st = &historyState{Version: historyStateVersion, Scope: a.historyScope()}
	}
	if st.Items == nil {
		st.Items = map[int]*historySnapshot{}
	}
	inScope := make(map[int]bool, len(ids))
	for _, id := range ids {
		inScope[id] = true
	}

	var docs []models.SourceDocument
	// Items whose history is read in full from their updates instead: they
	// came into the source's scope after the feed had passed their earlier
	// revisions, or the feed skipped some of their revisions (it is filtered
	// to the source's types, so an item that was another type for a while
	// has a gap).
	whole := map[int]bool{}
	// The feed is handled a page at a time, keeping only the source's items'
	// revisions: a project's whole history can be far bigger than them.
	token, err := a.readRevisionsFeed(ctx, client, org, project, apiVersion, st.Token, progress, func(revs []adoRevision) {
		for _, rev := range revs {
			if !inScope[rev.ID] || whole[rev.ID] {
				continue
			}
			snap := st.Items[rev.ID]
			if snap != nil && rev.Rev <= snap.Rev {
				continue // already recorded (the feed may repeat a revision)
			}
			if (snap == nil && rev.Rev > 1) || (snap != nil && rev.Rev > snap.Rev+1) {
				whole[rev.ID] = true
				docs = withoutItem(docs, a.src.ID, rev.ID)
				continue
			}
			doc, next := a.changeDocument(org, rev, snap)
			st.Items[rev.ID] = next
			if doc != nil {
				docs = append(docs, *doc)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if st.Items[id] == nil {
			whole[id] = true
		}
	}

	// Items no longer covered (deleted, moved to another area, type or
	// project) take their history records with them; items read in full
	// replace theirs.
	var removed []string
	for id := range st.Items {
		if !inScope[id] || whole[id] {
			removed = append(removed, strconv.Itoa(id))
			delete(st.Items, id)
		}
	}
	sort.Strings(removed)

	if len(whole) > 0 {
		histories, err := a.wholeHistories(ctx, client, org, project, apiVersion, whole, progress)
		if err != nil {
			return nil, err
		}
		missing := make([]int, 0, len(whole))
		for id := range whole {
			missing = append(missing, id)
		}
		sort.Ints(missing)
		for _, id := range missing {
			h := histories[id]
			var snap *historySnapshot
			for _, rev := range h.revs {
				doc, next := a.changeDocument(org, rev, snap)
				snap = next
				if doc != nil {
					docs = append(docs, *doc)
				}
			}
			if snap != nil {
				// Updates that only touched links still took a revision.
				snap.Rev = max(snap.Rev, h.lastRev)
				st.Items[id] = snap
			}
		}
	}

	st.Token = token
	state, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	run.result = &SyncResult{State: state, KeepHistory: !rebuild, RemoveHistoryIDs: removed}
	return docs, nil
}

// readRevisionsFeed reads the project's revisions feed from token ("" = from
// the beginning) to its end, handing each page's revisions to handle, and
// returns the token to resume from next time.
func (a *APISource) readRevisionsFeed(ctx context.Context, client *http.Client, org, project, apiVersion, token string,
	progress ProgressCallback, handle func([]adoRevision)) (string, error) {
	q := url.Values{}
	q.Set("includeLatestOnly", "false")
	q.Set("includeIdentityRef", "true")
	q.Set("$expand", "fields") // long text fields (descriptions) too
	q.Set("api-version", apiVersion)
	if types := parseWorkItemTypes(a.src.GetConfig("WorkItemTypes")); len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	base := fmt.Sprintf("%s/%s/_apis/wit/reporting/workitemrevisions", org, url.PathEscape(project))

	seen := 0
	for page := 1; ; page++ {
		if progress != nil {
			progress(models.SyncProgress{Phase: "fetching",
				Message: fmt.Sprintf("Reading work item history, page %d (%d revisions)", page, seen)})
		}
		if token != "" {
			q.Set("continuationToken", token)
		}
		data, err := a.getRaw(ctx, client, base+"?"+q.Encode())
		if err != nil {
			return "", fmt.Errorf("reading work item history: %w", err)
		}
		var resp struct {
			Values            []adoRevision `json:"values"`
			ContinuationToken string        `json:"continuationToken"`
			IsLastBatch       bool          `json:"isLastBatch"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return "", fmt.Errorf("work item history response is not valid JSON: %w", err)
		}
		seen += len(resp.Values)
		handle(resp.Values)
		stuck := resp.ContinuationToken == "" || (resp.ContinuationToken == token && len(resp.Values) == 0)
		if resp.ContinuationToken != "" {
			token = resp.ContinuationToken
		}
		if resp.IsLastBatch || stuck {
			return token, nil
		}
	}
}

// itemHistory is one item's whole history read from its updates.
type itemHistory struct {
	revs    []adoRevision
	lastRev int // the item's latest revision, field change or not
}

// wholeHistories reads the whole history of each item in ids from its
// updates, several items at a time. Any failure fails the sync, so no item
// is left with part of its history.
func (a *APISource) wholeHistories(ctx context.Context, client *http.Client, org, project, apiVersion string,
	ids map[int]bool, progress ProgressCallback) (map[int]itemHistory, error) {
	out := make(map[int]itemHistory, len(ids))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		done     int
		sem      = make(chan struct{}, diffFetchConcurrency)
	)
	for id := range ids {
		if workCtx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id int) {
			defer wg.Done()
			defer func() { <-sem }()
			h, err := a.workItemRevisionsFromUpdates(workCtx, client, org, project, apiVersion, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				cancel()
				return
			}
			out[id] = h
			done++
			if progress != nil {
				progress(models.SyncProgress{Phase: "fetching", Current: done, Total: len(ids),
					Message: fmt.Sprintf("Reading the whole history of work items %d/%d", done, len(ids))})
			}
		}(id)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// withoutItem drops item id's history records from docs: its history is
// being read in full instead.
func withoutItem(docs []models.SourceDocument, sourceID string, id int) []models.SourceDocument {
	prefix := fmt.Sprintf("%s_capi_%d_rev_", sourceID, id)
	out := docs[:0]
	for _, d := range docs {
		if !strings.HasPrefix(d.ID, prefix) {
			out = append(out, d)
		}
	}
	return out
}

// workItemRevisionsFromUpdates reads one item's whole history from its
// updates (each a revision's changed fields) and replays them into the full
// field set after every revision, the shape the revisions feed has.
func (a *APISource) workItemRevisionsFromUpdates(ctx context.Context, client *http.Client, org, project, apiVersion string,
	id int) (itemHistory, error) {
	base := fmt.Sprintf("%s/%s/_apis/wit/workItems/%d/updates?api-version=%s&$top=200",
		org, url.PathEscape(project), id, url.QueryEscape(apiVersion))
	var h itemHistory
	fields := map[string]any{}
	for skip := 0; ; skip += 200 {
		data, err := a.getRaw(ctx, client, base+"&$skip="+strconv.Itoa(skip))
		if err != nil {
			return h, fmt.Errorf("reading the history of work item %d: %w", id, err)
		}
		var page struct {
			Value []struct {
				Rev    int `json:"rev"`
				Fields map[string]struct {
					OldValue any `json:"oldValue"`
					NewValue any `json:"newValue"`
				} `json:"fields"`
			} `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return h, fmt.Errorf("work item %d updates response is not valid JSON: %w", id, err)
		}
		for _, u := range page.Value {
			h.lastRev = max(h.lastRev, u.Rev)
			if len(u.Fields) == 0 {
				continue // links or attachments only: no field revision
			}
			delete(fields, "System.History") // a comment belongs to its own revision
			for name, ch := range u.Fields {
				if ch.NewValue == nil {
					delete(fields, name)
				} else {
					fields[name] = ch.NewValue
				}
			}
			snapshot := make(map[string]any, len(fields))
			for k, v := range fields {
				snapshot[k] = v
			}
			h.revs = append(h.revs, adoRevision{ID: id, Rev: u.Rev, Fields: snapshot})
		}
		if len(page.Value) < 200 {
			return h, nil
		}
	}
}

// changeDocument compares rev with the item's previous state (nil: rev is
// where the item's history starts) and returns the change document — nil
// when only bookkeeping fields changed — and the item's state after rev.
func (a *APISource) changeDocument(org string, rev adoRevision, prev *historySnapshot) (*models.SourceDocument, *historySnapshot) {
	next := &historySnapshot{Rev: rev.Rev, Fields: map[string]historyValue{}}
	if prev != nil {
		next.Docs = prev.Docs
	}
	var lines []string
	names := make([]string, 0, len(rev.Fields))
	for name := range rev.Fields {
		if !historyIgnored(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var changed []string
	for _, name := range names {
		text := historyText(rev.Fields[name])
		val := historyValueOf(text, rev.Rev)
		var old historyValue
		hadOld := false
		if prev != nil {
			old, hadOld = prev.Fields[name]
		}
		if hadOld && old.same(val) {
			next.Fields[name] = old
			continue
		}
		if text == "" {
			// An empty field counts as absent: it is never kept, so a later
			// revision that drops it isn't reported as a change either.
			if hadOld {
				changed = append(changed, name)
				lines = append(lines, changeLine(name, old.V, "", oldRev(old)))
			}
			continue
		}
		next.Fields[name] = val
		changed = append(changed, name)
		switch {
		case prev == nil:
			lines = append(lines, fieldLine(name, text))
		case !hadOld:
			lines = append(lines, changeLine(name, "", text, 0))
		default:
			lines = append(lines, changeLine(name, old.V, text, oldRev(old)))
		}
	}
	if prev != nil {
		var cleared []string
		for name := range prev.Fields {
			if _, still := rev.Fields[name]; !still {
				cleared = append(cleared, name)
			}
		}
		sort.Strings(cleared)
		for _, name := range cleared {
			old := prev.Fields[name]
			changed = append(changed, name)
			lines = append(lines, changeLine(name, old.V, "", oldRev(old)))
		}
	}
	comment := historyText(rev.Fields["System.History"])
	if len(lines) == 0 && comment == "" {
		return nil, next
	}
	next.Docs++

	title := fieldString(rev.Fields, "System.Title")
	if title == "" {
		title = fmt.Sprintf("Work Item %d", rev.ID)
	}
	wiType := fieldString(rev.Fields, "System.WorkItemType")
	project := fieldString(rev.Fields, "System.TeamProject")
	if project == "" {
		project = a.src.GetConfig("AdoProject")
	}
	state := fieldString(rev.Fields, "System.State")
	changedBy := identityValues(rev.Fields["System.ChangedBy"])
	who := "someone"
	if len(changedBy) > 0 {
		who = changedBy[0]
	}
	when, _ := time.Parse(time.RFC3339, fieldString(rev.Fields, "System.ChangedDate"))

	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s — revision %d by %s", rev.ID, title, rev.Rev, who)
	if !when.IsZero() {
		fmt.Fprintf(&b, " on %s", when.UTC().Format("2006-01-02 15:04 UTC"))
	}
	b.WriteString("\n")
	if prev == nil {
		fmt.Fprintf(&b, "Created %s with:\n", strings.TrimSpace(wiType+" #"+strconv.Itoa(rev.ID)))
	} else if len(lines) > 0 {
		b.WriteString("Changed:\n")
	}
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
	if comment != "" {
		b.WriteString("Comment:\n" + comment + "\n")
	}

	props := map[string]string{
		"record": "change", "title": title, "type": wiType, "project": project, "state": state,
		"rev": strconv.Itoa(rev.Rev), "changed_by": who,
	}
	if !when.IsZero() {
		props["changed_date"] = when.UTC().Format(time.RFC3339)
	}
	if webURL := adoWorkItemWebURL(org, project, rev.ID); webURL != "" {
		props["web_url"] = webURL
	}

	var filters models.DocumentFilters
	filters.Add(models.FilterRecord, "change")
	filters.Add(models.FilterID, strconv.Itoa(rev.ID))
	filters.Add(models.FilterProject, project)
	filters.Add(models.FilterWorkItemType, wiType)
	filters.Add(models.FilterState, state)
	filters.Add(models.FilterChangedBy, changedBy...)
	for _, name := range changed {
		filters.Add(models.FilterChangedField, name, shortFieldName(name))
	}
	if comment != "" {
		filters.Add(models.FilterChangedField, "System.History", "Comment")
	}
	filters.Date = when

	return &models.SourceDocument{
		ID:          fmt.Sprintf("%s_capi_%d_rev_%d", a.src.ID, rev.ID, rev.Rev),
		Text:        strings.TrimSpace(b.String()),
		ChunkHeader: fmt.Sprintf("#%d %s — revision %d", rev.ID, title, rev.Rev),
		Tags:        map[string]string{"source_id": a.src.ID, "source_name": a.src.Name},
		Properties:  props,
		Filters:     filters,
	}, next
}

// historyText renders a field value as text: people by name, rich text as
// plain text.
func historyText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return stripHTML(t)
	case map[string]any:
		if ids := identityValues(t); len(ids) > 0 {
			if len(ids) > 1 && ids[1] != ids[0] {
				return ids[0] + " <" + ids[1] + ">"
			}
			return ids[0]
		}
		data, _ := json.Marshal(t)
		return string(data)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

func historyValueOf(text string, rev int) historyValue {
	if len(text) <= historyShortValue {
		return historyValue{V: text, Rev: rev}
	}
	sum := sha256.Sum256([]byte(text))
	return historyValue{H: hex.EncodeToString(sum[:16]), Rev: rev}
}

// oldRev is the revision whose change document has a long old value in
// full; 0 when the value is short and shown inline.
func oldRev(v historyValue) int {
	if v.H != "" {
		return v.Rev
	}
	return 0
}

func inline(s string) bool { return len(s) <= historyShortValue && !strings.Contains(s, "\n") }

func fieldLine(name, text string) string {
	if inline(text) {
		return name + ": " + text
	}
	return name + ":\n" + text
}

// changeLine renders one changed field. oldRev > 0 means the old value was
// long and is in that revision's change document rather than repeated here.
func changeLine(name, old, text string, oldRev int) string {
	from := old
	switch {
	case oldRev > 0:
		from = fmt.Sprintf("(previous value: see revision %d)", oldRev)
	case from == "":
		from = "(empty)"
	}
	to := text
	if to == "" {
		to = "(empty)"
	}
	if inline(from) && inline(to) && oldRev == 0 {
		return name + ": " + from + " → " + to
	}
	return name + ": " + from + " →\n" + to
}

// shortFieldName is a field's reference name without its namespace
// (Microsoft.VSTS.Scheduling.StoryPoints → StoryPoints), so a filter can use
// either.
func shortFieldName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}
