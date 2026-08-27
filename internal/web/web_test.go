package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/health"
	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/store"
	"github.com/MichalOndrejka/conduit/internal/syncctl"
	"github.com/MichalOndrejka/conduit/internal/syncsvc"
)

// ── fake Qdrant ──────────────────────────────────────────────────────────────

// fakeQdrant is a minimal in-memory Qdrant stand-in covering every endpoint
// the web package's handlers touch through *rag.VectorStore: collection
// list/create/delete, scroll, count, and delete-by-filter.
type fakeQdrant struct {
	mu sync.Mutex

	collections map[string]bool
	scroll      map[string][]map[string]any // collection -> points returned verbatim
	count       map[string]int              // collection -> points/count() response

	deleteFilterCalls []string // collections DeleteByFilter was called against
	createCalls       []string
	deleteCollCalls   []string
}

func newFakeQdrant() *fakeQdrant {
	return &fakeQdrant{
		collections: map[string]bool{},
		scroll:      map[string][]map[string]any{},
		count:       map[string]int{},
	}
}

func (q *fakeQdrant) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		path := r.URL.Path
		collOf := func(suffix string) string {
			name := strings.TrimPrefix(path, "/collections/")
			return strings.TrimSuffix(name, suffix)
		}
		switch {
		case r.Method == http.MethodGet && path == "/collections":
			var list []map[string]any
			for name := range q.collections {
				list = append(list, map[string]any{"name": name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"collections": list}})

		case r.Method == http.MethodPut && strings.HasPrefix(path, "/collections/") && !strings.Contains(path, "/points"):
			name := strings.TrimPrefix(path, "/collections/")
			q.collections[name] = true
			q.createCalls = append(q.createCalls, name)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})

		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/collections/"):
			name := strings.TrimPrefix(path, "/collections/")
			delete(q.collections, name)
			q.deleteCollCalls = append(q.deleteCollCalls, name)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/points/scroll"):
			name := collOf("/points/scroll")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{"points": q.scroll[name], "next_page_offset": nil},
			})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/points/count"):
			name := collOf("/points/count")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": q.count[name]}})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/points/delete"):
			name := collOf("/points/delete")
			q.deleteFilterCalls = append(q.deleteFilterCalls, name)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})

		case r.Method == http.MethodGet && strings.HasPrefix(path, "/collections/"):
			name := strings.TrimPrefix(path, "/collections/")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"points_count": q.count[name],
				"config":       map[string]any{"params": map[string]any{"vectors": map[string]any{"size": 3}}},
			}})

		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		}
	}
}

func fixedEmbedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}
}

// ── harness ──────────────────────────────────────────────────────────────────

type harness struct {
	t       *testing.T
	http    *httptest.Server
	client  *http.Client
	sources *store.SourceConfigStore
	secrets *secrets.Store
	cfg     *config.AppConfig
	qd      *fakeQdrant
	health  *health.Monitor
}

// newHarness wires a real *Server (real store/secrets/sync/health, real HTTP
// mux via s.Routes) against httptest fakes for Ollama and Qdrant — the same
// substitution pattern used in internal/syncsvc's tests, extended to cover
// the whole web layer. It waits for the health monitor to report both probes
// ready before returning, so handlers gated on health state behave
// deterministically.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := buildHarness(t, true)
	waitHealthReady(t, h.health)
	return h
}

// newHarnessNoHealthWait points Qdrant at an unreachable port and returns
// immediately without waiting for readiness, so s.health.Qdrant().Status
// deterministically stays "pending" (a single failed probe attempt doesn't
// flip the state — see health.Monitor.probe) for the handlers that branch on
// it (e.g. handleMapData's Qdrant-unreachable path).
func newHarnessNoHealthWait(t *testing.T) *harness {
	t.Helper()
	return buildHarness(t, false)
}

func buildHarness(t *testing.T, qdrantReachable bool) *harness {
	t.Helper()

	embedSrv := httptest.NewServer(fixedEmbedHandler())
	t.Cleanup(embedSrv.Close)

	qd := newFakeQdrant()
	qdHost, qdPort := "127.0.0.1", 1 // reserved port, connection refused
	if qdrantReachable {
		qdSrv := httptest.NewServer(qd.handler())
		t.Cleanup(qdSrv.Close)
		qdURL, err := url.Parse(qdSrv.URL)
		if err != nil {
			t.Fatal(err)
		}
		qdHost = qdURL.Hostname()
		qdPort, _ = strconv.Atoi(qdURL.Port())
	}

	dataDir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG", filepath.Join(dataDir, "config.json"))

	cfg := &config.AppConfig{}
	cfg.Embedding.Provider = "openai-compatible"
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Embedding.Dimensions = 3
	cfg.Embedding.Model = "test-model"
	cfg.Qdrant.Host = qdHost
	cfg.Qdrant.Port = qdPort
	cfg.Chunking.MaxChunkSize = 2000
	cfg.Chunking.Overlap = 200
	cfg.Preprocessing.SourceTypes = map[string]bool{}

	sourceStore := store.NewSourceConfigStore(filepath.Join(dataDir, "conduit-sources.json"))
	secretsStore, err := secrets.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg, secretsStore)
	memSvc := memory.NewService(vectors, embedding)
	chunker := rag.NewTextChunker(cfg)
	indexer := rag.NewDocumentIndexer(vectors, embedding, chunker)
	syncSvc := syncsvc.New(cfg, sourceStore, secretsStore, indexer, syncctl.NewProgressStore(), syncctl.NewControlStore())

	healthMon := health.Start(cfg, vectors, embedding)

	srv := NewServer(cfg, sourceStore, vectors, memSvc, secretsStore, syncSvc, healthMon)
	mux := http.NewServeMux()
	srv.Routes(mux)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}

	return &harness{
		t: t, http: httpSrv, client: client,
		sources: sourceStore, secrets: secretsStore, cfg: cfg, qd: qd, health: healthMon,
	}
}

func waitHealthReady(t *testing.T, mon *health.Monitor) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mon.IsReady() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("health monitor never became ready against the fake Qdrant/embedding servers")
}

func (h *harness) url(path string) string { return h.http.URL + path }

func (h *harness) get(path string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Get(h.url(path))
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) postForm(path string, values url.Values) *http.Response {
	h.t.Helper()
	resp, err := h.client.PostForm(h.url(path), values)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

// postMultipart POSTs values as multipart/form-data, for handlers that must
// use r.ParseMultipartForm rather than r.ParseForm (see handleSettingsVerify's
// comment on why the verify forms are submitted this way).
func (h *harness) postMultipart(path string, values url.Values) *http.Response {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for key, vs := range values {
		for _, v := range vs {
			if err := mw.WriteField(key, v); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	if err := mw.Close(); err != nil {
		h.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, h.url(path), &buf)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
