// Package stats records what Conduit does (searches the agent makes, sync
// runs) and summarizes it for the Stats page. A recorder from Open also
// appends every event to a file in the data folder, so stats survive
// restarts; events older than the retention are dropped.
package stats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// FileName is the stats file in the data folder: one JSON event per line.
const FileName = "stats.jsonl"

// Event is one recorded tool call or sync run.
type Event struct {
	Time time.Time `json:"time"`
	Type string    `json:"type"` // tool | sync
	Tool *ToolCall `json:"tool,omitempty"`
	Sync *SyncRun  `json:"sync,omitempty"`
}

// ToolCall is one MCP tool call.
type ToolCall struct {
	Name string `json:"name"`
	Mode string `json:"mode,omitempty"` // semantic_search, pattern_search, retrieve_chunk, …
	// Collection is the searched collection (search tools).
	Collection string              `json:"collection,omitempty"`
	Query      string              `json:"query,omitempty"`
	Filters    map[string][]string `json:"filters,omitempty"`
	Page       int                 `json:"page,omitempty"`
	// Results is how many results came back; Counted says whether the tool
	// reported it (only search and experience tools do).
	Results int  `json:"results"`
	Counted bool `json:"counted,omitempty"`
	HasMore bool `json:"has_more,omitempty"`
	// Hits are the situations of returned experience entries.
	Hits      []string `json:"hits,omitempty"`
	LatencyMs int64    `json:"ms"`
	// Cold marks the process's first call that embeds a query: it includes
	// the embedding model loading, so it's left out of latency figures.
	Cold  bool   `json:"cold,omitempty"`
	Error string `json:"error,omitempty"`

	// Requests are the HTTP requests the call made to a source's API (live
	// tools: pull requests, pipeline runs); RateLimited how many of them the
	// API refused for its rate limit.
	Requests    int `json:"requests,omitempty"`
	RateLimited int `json:"rate_limited,omitempty"`
	// SourceID, SourceName and Provider name the configured source whose
	// API the call read, when it read one.
	SourceID   string `json:"source_id,omitempty"`
	SourceName string `json:"source_name,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

// SyncRun is one source's sync.
type SyncRun struct {
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	SourceType string `json:"source_type"`
	Provider   string `json:"provider,omitempty"`
	Status     string `json:"status"` // completed | failed | cancelled
	ErrorPhase string `json:"error_phase,omitempty"`
	Error      string `json:"error,omitempty"`

	DurationMs   int64 `json:"ms"`
	FetchMs      int64 `json:"fetch_ms"`
	PreprocessMs int64 `json:"preprocess_ms,omitempty"`
	IndexMs      int64 `json:"index_ms"`

	Documents int `json:"documents"`
	Chunks    int `json:"chunks"`
	// What the incremental sync did with the documents: embedded new ones
	// (Added) and changed ones (Updated), rewrote the payload of those whose
	// metadata alone changed, left Unchanged ones as they were, and deleted
	// the ones the source no longer has. NotRefetched counts the documents
	// whose fetch was skipped as provably current; EmbeddedChunks the chunks
	// embedded (Chunks is the source's total after the sync).
	Added          int `json:"added,omitempty"`
	Updated        int `json:"updated,omitempty"`
	MetadataOnly   int `json:"metadata_only,omitempty"`
	Unchanged      int `json:"unchanged,omitempty"`
	Deleted        int `json:"deleted,omitempty"`
	NotRefetched   int `json:"not_refetched,omitempty"`
	EmbeddedChunks int `json:"embedded_chunks,omitempty"`
	// Requests are the HTTP requests the fetch made to the source's API;
	// RateLimited how many of them the API refused for its rate limit
	// (retries included).
	Requests    int `json:"requests"`
	RateLimited int `json:"rate_limited,omitempty"`
}

// Recorder keeps events in memory and, when opened on a file, appends each
// one to it. A nil *Recorder records nothing, so stats stay optional
// everywhere it's passed.
type Recorder struct {
	started time.Time

	mu     sync.Mutex
	events []Event

	// Persistence (Open): the file events are appended to, how long they
	// are kept, and when old ones were last dropped.
	path      string
	file      *os.File
	retention time.Duration
	pruned    time.Time
	writeErr  bool // a write failure was logged; don't repeat it per event

	embedded atomic.Bool // a query has been embedded by this process
}

// New returns an empty in-memory recorder started now.
func New() *Recorder {
	return &Recorder{started: time.Now().UTC()}
}

// Open returns a recorder that keeps its events in path: the events of
// earlier runs from the last retention are loaded, older ones dropped, and
// every new event is appended as it's recorded. Lines that can't be read are
// skipped.
func Open(path string, retention time.Duration) (*Recorder, error) {
	r := &Recorder{started: time.Now().UTC(), path: path, retention: retention}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cutoff := r.started.Add(-retention)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	skipped := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil || (e.Tool == nil && e.Sync == nil) {
			skipped++
			continue
		}
		if e.Time.After(cutoff) {
			r.events = append(r.events, e)
		}
	}
	if skipped > 0 {
		log.Printf("warning: %s: skipped %d unreadable lines", path, skipped)
	}
	slices.SortStableFunc(r.events, func(a, b Event) int { return a.Time.Compare(b.Time) })
	if len(r.events) > 0 {
		r.started = r.events[0].Time
	}
	// Rewrite without the dropped events, then append from here on.
	if err := r.rewrite(); err != nil {
		return nil, err
	}
	r.pruned = time.Now()
	return r, nil
}

// Close closes the stats file.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Started is when the recorded stats begin: the oldest kept event, or when
// recording began.
func (r *Recorder) Started() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// Reset drops every recorded event, from memory and the stats file.
func (r *Recorder) Reset() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
	r.started = time.Now().UTC()
	if r.path == "" {
		return nil
	}
	return r.rewrite()
}

// RecordTool records a tool call.
func (r *Recorder) RecordTool(tc *ToolCall) {
	if r != nil && tc != nil {
		r.add(Event{Type: "tool", Tool: tc})
	}
}

// RecordSync records a sync run.
func (r *Recorder) RecordSync(s *SyncRun) {
	if r != nil && s != nil {
		r.add(Event{Type: "sync", Sync: s})
	}
}

// firstEmbedding reports whether this is the process's first call that
// embeds a query.
func (r *Recorder) firstEmbedding() bool {
	return r != nil && !r.embedded.Swap(true)
}

func (r *Recorder) add(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	if r.path == "" {
		return
	}
	// Drop expired events about once an hour, so a long-running process
	// keeps to the retention too.
	if time.Since(r.pruned) > time.Hour {
		r.pruned = time.Now()
		cutoff := e.Time.Add(-r.retention)
		if i := slices.IndexFunc(r.events, func(x Event) bool { return x.Time.After(cutoff) }); i > 0 {
			r.events = slices.Delete(r.events, 0, i)
			r.started = r.events[0].Time
			r.logWrite(r.rewrite())
			return
		}
	}
	r.logWrite(r.appendLine(e))
}

// appendLine writes e to the stats file.
func (r *Recorder) appendLine(e Event) error {
	if r.file == nil {
		return fmt.Errorf("stats file is closed")
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = r.file.Write(append(line, '\n'))
	return err
}

// rewrite replaces the stats file with the events in memory and reopens it
// for appending. Called with r.mu held (or before r is shared).
func (r *Recorder) rewrite() error {
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
	var buf bytes.Buffer
	for _, e := range r.events {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	return nil
}

// logWrite logs the first stats-file failure; the stats stay in memory.
func (r *Recorder) logWrite(err error) {
	if err != nil && !r.writeErr {
		r.writeErr = true
		log.Printf("warning: writing %s: %v (stats stay in memory until restart)", r.path, err)
	} else if err == nil {
		r.writeErr = false
	}
}

// Events returns a copy of every event recorded so far, oldest first.
func (r *Recorder) Events() []Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}
