package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// runSearchCLI's error paths call log.Fatal, which terminates the whole test
// binary if invoked in-process. Following Go's standard subprocess-helper
// pattern (see os/exec's own TestHelperProcess), those paths are exercised by
// re-executing this test binary with an env var telling it which scenario to
// run, and asserting on the child process's exit status and output.
const (
	subprocessEnvVar = "CONDUIT_MAIN_TEST_SUBPROCESS"
	scenarioUsage    = "usage-error"
	scenarioSearch   = "search-error"
)

func TestMain(m *testing.M) {
	switch os.Getenv(subprocessEnvVar) {
	case scenarioUsage:
		runSearchCLI(nil, []string{"only-one-arg"})
		os.Exit(0) // unreachable if runSearchCLI's log.Fatal fired, as expected
	case scenarioSearch:
		runSearchCLISearchErrorSubprocess()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runSearchCLISearchErrorSubprocess() {
	// The embedding endpoint URL is passed in by the parent process so this
	// subprocess talks to the same fake server.
	cfg := &config.AppConfig{}
	cfg.Embedding.Provider = "openai-compatible"
	cfg.Embedding.BaseURL = os.Getenv("CONDUIT_TEST_EMBED_URL")
	cfg.Embedding.MaxInputTokens = 8192
	embedding := rag.NewEmbeddingService(cfg, nil)

	qdrantURL := os.Getenv("CONDUIT_TEST_QDRANT_URL")
	u, err := url.Parse(qdrantURL)
	if err != nil {
		panic(err)
	}
	port, _ := strconv.Atoi(u.Port())
	vCfg := &config.AppConfig{}
	vCfg.Qdrant.Host = u.Hostname()
	vCfg.Qdrant.Port = port
	vectors := rag.NewVectorStore(vCfg)

	searchSvc := rag.NewSearchService(vectors, embedding, nil)
	runSearchCLI(searchSvc, []string{"conduit_workitems", "some", "query"})
}

func runSelfAsSubprocess(t *testing.T, scenario string, extraEnv ...string) (exitErr error, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestMain")
	cmd.Env = append(os.Environ(), subprocessEnvVar+"="+scenario)
	cmd.Env = append(cmd.Env, extraEnv...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return err, outBuf.String(), errBuf.String()
}

func TestRunSearchCLIExitsNonZeroOnUsageError(t *testing.T) {
	err, _, stderr := runSelfAsSubprocess(t, scenarioUsage)
	if err == nil {
		t.Fatal("expected the subprocess to exit with a non-zero status")
	}
	if !strings.Contains(stderr, "usage: conduit search") {
		t.Errorf("stderr = %q, want it to contain the usage message", stderr)
	}
}

func TestRunSearchCLIExitsNonZeroOnSearchError(t *testing.T) {
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "embedding unavailable"}`))
	}))
	defer embed.Close()
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result": {"points": []}}`))
	}))
	defer qdrant.Close()

	err, _, stderr := runSelfAsSubprocess(t, scenarioSearch,
		"CONDUIT_TEST_EMBED_URL="+embed.URL,
		"CONDUIT_TEST_QDRANT_URL="+qdrant.URL,
	)
	if err == nil {
		t.Fatal("expected the subprocess to exit with a non-zero status")
	}
	if !strings.Contains(stderr, "search:") {
		t.Errorf("stderr = %q, want it to contain the search error prefix", stderr)
	}
}

// TestRunSearchCLIPrintsResultsOnSuccess runs in-process since the success
// path returns normally instead of calling log.Fatal.
func TestRunSearchCLIPrintsResultsOnSuccess(t *testing.T) {
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}))
	defer embed.Close()
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"points": []map[string]any{
					{"id": "hit-1", "score": 0.5, "payload": map[string]any{"text": "matched text"}},
				},
			},
		})
	}))
	defer qdrant.Close()

	cfg := &config.AppConfig{}
	cfg.Embedding.Provider = "openai-compatible"
	cfg.Embedding.BaseURL = embed.URL
	cfg.Embedding.MaxInputTokens = 8192
	embedding := rag.NewEmbeddingService(cfg, nil)

	u, err := url.Parse(qdrant.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	vCfg := &config.AppConfig{}
	vCfg.Qdrant.Host = u.Hostname()
	vCfg.Qdrant.Port = port
	vectors := rag.NewVectorStore(vCfg)

	searchSvc := rag.NewSearchService(vectors, embedding, nil)

	stdout := captureStdout(t, func() {
		runSearchCLI(searchSvc, []string{"conduit_workitems", "some", "query"})
	})
	if !strings.Contains(stdout, "matched text") {
		t.Errorf("stdout = %q, want it to contain the matched result text", stdout)
	}
	if !strings.Contains(stdout, "hit-1") {
		t.Errorf("stdout = %q, want it to contain the result ID", stdout)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	var buf strings.Builder
	buf.Grow(4096)
	_, _ = io.Copy(&buf, r)
	return buf.String()
}
