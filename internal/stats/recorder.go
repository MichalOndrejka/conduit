package stats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// FileName is the stats file in the data directory: one JSON event per
// line, appended as events happen.
const FileName = "conduit-stats.jsonl"

// DefaultKeepBuilds is how many builds' history the file keeps; older
// builds are dropped whole, so every kept build stays complete.
const DefaultKeepBuilds = 30

// Event is one line of the stats file.
type Event struct {
	Time  time.Time `json:"t"`
	Build string    `json:"b"`
	Type  string    `json:"type"` // start | tool | sync
	Start *Build    `json:"start,omitempty"`
	Tool  *ToolCall `json:"tool,omitempty"`
	Sync  *SyncRun  `json:"sync,omitempty"`
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

// Recorder appends events to the stats file. A nil *Recorder records
// nothing, so stats stay optional everywhere it's passed.
type Recorder struct {
	path  string
	build Build

	mu sync.Mutex
	f  *os.File

	embedded atomic.Bool // a query has been embedded by this process
}

// Open opens (creating if needed) the stats file in dir, drops builds beyond
// the newest keepBuilds, and records this process's start.
func Open(dir string, build Build, keepBuilds int) (*Recorder, error) {
	path := filepath.Join(dir, FileName)
	if keepBuilds > 0 {
		if err := prune(path, build.ID, keepBuilds); err != nil {
			log.Printf("warning: trimming stats history: %v", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	r := &Recorder{path: path, build: build, f: f}
	b := build
	r.write(Event{Type: "start", Start: &b})
	return r, nil
}

// Build is the running build.
func (r *Recorder) Build() Build {
	if r == nil {
		return Build{}
	}
	return r.build
}

// Path is the stats file.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Close closes the file.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// RecordTool appends a tool call.
func (r *Recorder) RecordTool(tc *ToolCall) {
	if r != nil && tc != nil {
		r.write(Event{Type: "tool", Tool: tc})
	}
}

// RecordSync appends a sync run.
func (r *Recorder) RecordSync(s *SyncRun) {
	if r != nil && s != nil {
		r.write(Event{Type: "sync", Sync: s})
	}
}

// firstEmbedding reports whether this is the process's first call that
// embeds a query.
func (r *Recorder) firstEmbedding() bool {
	return r != nil && !r.embedded.Swap(true)
}

// write appends one event as a single line. Each write goes straight to the
// file (no buffering), so a process killed by a hot reload loses nothing.
func (r *Recorder) write(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Build = r.build.ID
	line, err := json.Marshal(e)
	if err != nil {
		log.Printf("warning: encoding stats event: %v", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Write(append(line, '\n')); err != nil {
		log.Printf("warning: writing stats event: %v", err)
	}
}

// Load reads every event in the file. Lines that don't parse (e.g. a line
// cut short by a crash mid-write) are skipped.
func (r *Recorder) Load() ([]Event, error) {
	if r == nil {
		return nil, nil
	}
	return ReadFile(r.path)
}

// ReadFile reads a stats file.
func ReadFile(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var events []Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Build != "" {
			events = append(events, e)
		}
	}
	return events, sc.Err()
}

// prune rewrites the file keeping only the newest keep builds, current
// included, by when each build was first seen.
func prune(path, current string, keep int) error {
	events, err := ReadFile(path)
	if err != nil || len(events) == 0 {
		return err
	}
	var order []string
	seen := map[string]bool{}
	for _, e := range events {
		if !seen[e.Build] {
			seen[e.Build] = true
			order = append(order, e.Build)
		}
	}
	if !seen[current] {
		order = append(order, current)
	}
	if len(order) <= keep {
		return nil
	}
	kept := map[string]bool{}
	for _, b := range order[len(order)-keep:] {
		kept[b] = true
	}
	var buf bytes.Buffer
	for _, e := range events {
		if !kept[e.Build] {
			continue
		}
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
