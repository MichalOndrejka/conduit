package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOllama serves the endpoints setup touches. present lists downloaded
// models; vram maps a model to the fraction of it /api/ps reports in VRAM.
type fakeOllama struct {
	mu        sync.Mutex
	present   map[string]bool
	vram      map[string]float64
	pullError string
	pulled    []string
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"0.12.0"}`)
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		name := modelOf(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.present[name] {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"model '%s' not found"}`, name)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		name := modelOf(r)
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		if f.pullError != "" {
			fmt.Fprintf(w, "{\"error\":%q}\n", f.pullError)
			return
		}
		for _, done := range []int{0, 50, 100} {
			fmt.Fprintf(w, `{"status":"pulling abc","total":100,"completed":%d}`+"\n", done)
		}
		fmt.Fprintln(w, `{"status":"success"}`)
		f.mu.Lock()
		f.present[name] = true
		f.pulled = append(f.pulled, name)
		f.mu.Unlock()
	})
	load := func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) }
	mux.HandleFunc("POST /api/embed", load)
	mux.HandleFunc("POST /api/generate", load)
	mux.HandleFunc("GET /api/ps", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		type entry struct {
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			SizeVRAM int64  `json:"size_vram"`
		}
		var ms []entry
		for name, frac := range f.vram {
			ms = append(ms, entry{Name: withTag(name), Size: 1000, SizeVRAM: int64(frac * 1000)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": ms})
	})
	return mux
}

func modelOf(r *http.Request) string {
	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body.Model
}

func newEnv(t *testing.T, f *fakeOllama) (ollamaURL, qdrantURL string) {
	t.Helper()
	ol := httptest.NewServer(f.handler())
	t.Cleanup(ol.Close)
	qd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"result":{"collections":[]}}`)
	}))
	t.Cleanup(qd.Close)
	return ol.URL, qd.URL
}

func run(t *testing.T, f *fakeOllama, gpuExpected string) (string, error) {
	t.Helper()
	ollamaURL, qdrantURL := newEnv(t, f)
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		QdrantURL: qdrantURL,
		Models: []Model{
			{Name: "embed", OllamaURL: ollamaURL, Embedding: true},
			{Name: "llm:3b", OllamaURL: ollamaURL},
		},
		GPUExpected: gpuExpected,
		Out:         &out,
	})
	return out.String(), err
}

func TestRunPullsOnlyMissingModels(t *testing.T) {
	f := &fakeOllama{
		present: map[string]bool{"embed": true},
		vram:    map[string]float64{"embed": 1, "llm:3b": 1},
	}
	out, err := run(t, f, "")
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out)
	}
	f.mu.Lock()
	pulled := f.pulled
	f.mu.Unlock()
	if len(pulled) != 1 || pulled[0] != "llm:3b" {
		t.Errorf("pulled = %v, want only llm:3b", pulled)
	}
	for _, want := range []string{"embed already downloaded", "llm:3b downloaded", "100% of 100 B", "Setup done."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunGPUPolicy(t *testing.T) {
	cases := []struct {
		name        string
		vram        float64
		gpuExpected string
		wantErr     bool
		wantOut     string
	}{
		{"gpu, overlay", 1, "nvidia", false, "runs on GPU"},
		{"cpu, overlay", 0, "nvidia", true, "nvidia GPU overlay is in use"},
		{"partial, overlay", 0.5, "amd", true, "50% GPU / 50% CPU"},
		{"cpu, no overlay", 0, "", false, "runs on CPU"},
		{"partial, no overlay", 0.5, "", false, "doesn't fit in VRAM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeOllama{
				present: map[string]bool{"embed": true, "llm:3b": true},
				vram:    map[string]float64{"embed": tc.vram, "llm:3b": tc.vram},
			}
			out, err := run(t, f, tc.gpuExpected)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v\n%s", err, tc.wantErr, out)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output missing %q:\n%s", tc.wantOut, out)
			}
		})
	}
}

func TestRunPullErrorGivesCertHint(t *testing.T) {
	f := &fakeOllama{
		present:   map[string]bool{},
		vram:      map[string]float64{},
		pullError: "tls: failed to verify certificate: x509: certificate signed by unknown authority",
	}
	out, err := run(t, f, "")
	if err == nil {
		t.Fatalf("expected failure\n%s", out)
	}
	if !strings.Contains(out, "CONDUIT_CERTS_DIR") || !strings.Contains(out, "Conduit will not start") {
		t.Errorf("missing cert hint or failure verdict:\n%s", out)
	}
}

func TestRunUnreachableServicesFail(t *testing.T) {
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		QdrantURL: "http://127.0.0.1:1",
		Models:    []Model{{Name: "embed", OllamaURL: "http://127.0.0.1:1", Embedding: true}},
		Out:       &out,
	})
	if err == nil {
		t.Fatalf("expected failure\n%s", out.String())
	}
	for _, want := range []string{"Qdrant unreachable", "Ollama unreachable"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestOllamaURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://ollama:11434/v1":  "http://ollama:11434",
		"http://ollama:11434/v1/": "http://ollama:11434",
		"http://ollama:11434":     "http://ollama:11434",
	} {
		if got := OllamaURL(in); got != want {
			t.Errorf("OllamaURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithTag(t *testing.T) {
	for in, want := range map[string]string{
		"nomic":             "nomic:latest",
		"llama3.2:3b":       "llama3.2:3b",
		"host:5000/m":       "host:5000/m:latest",
		"host:5000/m:1b":    "host:5000/m:1b",
		"library/qwen:0.5b": "library/qwen:0.5b",
	} {
		if got := withTag(in); got != want {
			t.Errorf("withTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPullHint(t *testing.T) {
	if pullHint(errors.New("something odd")) != "" {
		t.Error("unexpected hint for unknown error")
	}
	if !strings.Contains(pullHint(errors.New("dial tcp: lookup registry.ollama.ai: no such host")), "HTTPS_PROXY") {
		t.Error("expected proxy hint for DNS failure")
	}
}
