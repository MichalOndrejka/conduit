// Package stats records what the running Conduit process does (searches
// the agent makes, sync runs) in memory and summarizes it for the Stats
// page. Nothing is persisted: a restart starts with empty stats.
package stats

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Event is one recorded tool call or sync run.
type Event struct {
	Time time.Time
	Type string // tool | sync
	Tool *ToolCall
	Sync *SyncRun
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
	// Requests are the HTTP requests the fetch made to the source's API;
	// RateLimited how many of them the API refused for its rate limit
	// (retries included).
	Requests    int `json:"requests"`
	RateLimited int `json:"rate_limited,omitempty"`
}

// Recorder keeps this process's events in memory. A nil *Recorder records
// nothing, so stats stay optional everywhere it's passed.
type Recorder struct {
	started time.Time

	mu     sync.Mutex
	events []Event

	embedded atomic.Bool // a query has been embedded by this process
}

// New returns an empty recorder started now.
func New() *Recorder {
	return &Recorder{started: time.Now().UTC()}
}

// Started is when recording began (the process start).
func (r *Recorder) Started() time.Time {
	if r == nil {
		return time.Time{}
	}
	return r.started
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
