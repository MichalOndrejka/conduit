// Package setup implements `conduit setup`, the one-shot docker-compose step
// that runs before Conduit starts: it checks Qdrant and Ollama are reachable,
// pulls any configured model that's missing, loads each one, and reports
// whether it runs on GPU or CPU.
//
// The verdict is printed for the person watching `docker compose up`, and the
// exit status gates Conduit's start: a GPU overlay that ends up on CPU is an
// error (the user asked for a GPU and didn't get one), while CPU without an
// overlay is only a warning.
package setup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Model is one model to prepare. Embedding models are loaded via /api/embed,
// chat models via /api/generate.
type Model struct {
	Name      string
	OllamaURL string // native API base, e.g. http://ollama:11434
	Embedding bool
}

type Options struct {
	QdrantURL    string
	QdrantAPIKey string
	Models       []Model
	// GPUExpected is set by a GPU compose overlay ("nvidia", "amd"); empty
	// means CPU was not ruled out.
	GPUExpected string
	Out         io.Writer
	Client      *http.Client
}

// OllamaURL turns an OpenAI-compatible base URL (http://ollama:11434/v1)
// into Ollama's native API base.
func OllamaURL(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}

type report struct {
	out      io.Writer
	warnings int
	errors   int
}

func (r *report) ok(format string, a ...any) { fmt.Fprintf(r.out, "  [ok]   "+format+"\n", a...) }
func (r *report) info(format string, a ...any) {
	fmt.Fprintf(r.out, "  ...    "+format+"\n", a...)
}
func (r *report) warn(format string, a ...any) {
	r.warnings++
	fmt.Fprintf(r.out, "  [warn] "+format+"\n", a...)
}
func (r *report) fail(format string, a ...any) {
	r.errors++
	fmt.Fprintf(r.out, "  [FAIL] "+format+"\n", a...)
}

// Run performs all checks and returns an error if Conduit shouldn't start.
func Run(ctx context.Context, opts Options) error {
	r := &report{out: opts.Out}
	c := opts.Client
	if c == nil {
		c = &http.Client{}
	}
	fmt.Fprintln(r.out, "Conduit setup")

	if err := getJSON(ctx, c, opts.QdrantURL+"/collections", opts.QdrantAPIKey, nil); err != nil {
		r.fail("Qdrant unreachable at %s: %v", opts.QdrantURL, err)
	} else {
		r.ok("Qdrant reachable at %s", opts.QdrantURL)
	}

	reachable := map[string]bool{}
	for _, m := range opts.Models {
		if _, seen := reachable[m.OllamaURL]; !seen {
			var v struct {
				Version string `json:"version"`
			}
			err := getJSON(ctx, c, m.OllamaURL+"/api/version", "", &v)
			reachable[m.OllamaURL] = err == nil
			if err != nil {
				r.fail("Ollama unreachable at %s: %v", m.OllamaURL, err)
			} else {
				r.ok("Ollama %s reachable at %s", v.Version, m.OllamaURL)
			}
		}
		if reachable[m.OllamaURL] {
			prepareModel(ctx, c, r, m, opts.GPUExpected)
		}
	}

	switch {
	case r.errors > 0:
		fmt.Fprintf(r.out, "Setup FAILED: %d error(s), %d warning(s). Conduit will not start.\n", r.errors, r.warnings)
		return fmt.Errorf("%d setup error(s)", r.errors)
	case r.warnings > 0:
		fmt.Fprintf(r.out, "Setup done with %d warning(s).\n", r.warnings)
	default:
		fmt.Fprintln(r.out, "Setup done.")
	}
	return nil
}

func prepareModel(ctx context.Context, c *http.Client, r *report, m Model, gpuExpected string) {
	present, err := modelPresent(ctx, c, m)
	if err != nil {
		r.fail("%s: checking model: %v", m.Name, err)
		return
	}
	if present {
		r.ok("%s already downloaded", m.Name)
	} else {
		r.info("%s: downloading (first start only — can take several minutes)", m.Name)
		if err := pull(ctx, c, r, m); err != nil {
			r.fail("%s: download failed: %v%s", m.Name, err, pullHint(err))
			return
		}
		r.ok("%s downloaded", m.Name)
	}

	r.info("%s: loading to check GPU/CPU placement", m.Name)
	if err := load(ctx, c, m); err != nil {
		r.fail("%s: loading failed: %v", m.Name, err)
		return
	}
	size, vram, found, err := placement(ctx, c, m)
	switch {
	case err != nil:
		r.warn("%s: couldn't read placement: %v", m.Name, err)
	case !found:
		r.warn("%s: not listed by /api/ps after loading; placement unknown", m.Name)
	default:
		judge(r, m.Name, size, vram, gpuExpected)
	}
}

// judge applies the GPU policy: full GPU is fine; anything less is an error
// when a GPU overlay is in use and a warning otherwise.
func judge(r *report, name string, size, vram int64, gpuExpected string) {
	gpuPct := 0
	if size > 0 {
		gpuPct = int(vram * 100 / size)
	}
	where := fmt.Sprintf("%d%% GPU / %d%% CPU", gpuPct, 100-gpuPct)
	switch {
	case size > 0 && vram >= size:
		r.ok("%s runs on GPU", name)
	case gpuExpected != "":
		r.fail("%s runs %s, but the %s GPU overlay is in use. Check the GPU driver "+
			"(and nvidia-container-toolkit on Linux), free VRAM, or choose a smaller model; "+
			"drop the overlay to run on CPU on purpose", name, where, gpuExpected)
	case vram == 0:
		r.warn("%s runs on CPU — syncs will be slow. For a GPU, add docker-compose.nvidia.yml "+
			"(or docker-compose.amd.yml on Linux)", name)
	default:
		r.warn("%s runs %s — the model doesn't fit in VRAM; syncs will be slower", name, where)
	}
}

func modelPresent(ctx context.Context, c *http.Client, m Model) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	resp, err := post(ctx, c, m.OllamaURL+"/api/show", map[string]any{"model": m.Name, "name": m.Name})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// pull streams /api/pull progress, printing each new status and every 10%
// of each layer download.
func pull(ctx context.Context, c *http.Client, r *report, m Model) error {
	resp, err := post(ctx, c, m.OllamaURL+"/api/pull", map[string]any{"model": m.Name, "name": m.Name})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}

	lastStatus, lastDecile := "", -1
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Error != "" {
			return errors.New(ev.Error)
		}
		if ev.Status != lastStatus {
			lastStatus, lastDecile = ev.Status, -1
			if ev.Total == 0 {
				r.info("%s: %s", m.Name, ev.Status)
			}
		}
		if ev.Total > 0 {
			if d := int(ev.Completed * 10 / ev.Total); d > lastDecile {
				lastDecile = d
				r.info("%s: %s %d%% of %s", m.Name, ev.Status, d*10, humanBytes(ev.Total))
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if lastStatus != "success" {
		return fmt.Errorf("stream ended without success (last status %q)", lastStatus)
	}
	return nil
}

// pullHint points at the usual fix for download failures behind a
// corporate network.
func pullHint(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
		return "\n         → TLS interception: export your CAs (scripts/export-ca-certs.ps1) and set " +
			"CONDUIT_CERTS_DIR to that folder (see docs/installation.md)"
	case strings.Contains(msg, "proxy") || strings.Contains(msg, "407"):
		return "\n         → check HTTPS_PROXY (and proxy credentials)"
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "403"):
		return "\n         → no direct internet access? set HTTPS_PROXY to your corporate proxy"
	case strings.Contains(msg, "file does not exist") || strings.Contains(msg, "not found"):
		return "\n         → check the model name (EMBEDDING_MODEL / PREPROCESSING_MODEL)"
	}
	return ""
}

func load(ctx context.Context, c *http.Client, m Model) error {
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	path, body := "/api/generate", map[string]any{"model": m.Name, "prompt": "", "stream": false}
	if m.Embedding {
		path, body = "/api/embed", map[string]any{"model": m.Name, "input": "conduit setup check"}
	}
	resp, err := post(ctx, c, m.OllamaURL+path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// placement reads /api/ps for the model's total and in-VRAM size.
func placement(ctx context.Context, c *http.Client, m Model) (size, vram int64, found bool, err error) {
	var ps struct {
		Models []struct {
			Name     string `json:"name"`
			Model    string `json:"model"`
			Size     int64  `json:"size"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := getJSON(ctx, c, m.OllamaURL+"/api/ps", "", &ps); err != nil {
		return 0, 0, false, err
	}
	want := withTag(m.Name)
	for _, p := range ps.Models {
		if withTag(p.Name) == want || withTag(p.Model) == want {
			return p.Size, p.SizeVRAM, true, nil
		}
	}
	return 0, 0, false, nil
}

// withTag normalizes "model" to "model:latest", as Ollama names it.
func withTag(name string) string {
	if i := strings.LastIndex(name, "/"); strings.Contains(name[i+1:], ":") {
		return name
	}
	return name + ":latest"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ── HTTP helpers ────────────────────────────────────────────────────────────

// Pulls are bounded only by ctx, since a multi-GB download legitimately
// takes long. Loading a model on CPU can take minutes; reachability and
// status calls should answer quickly.
const (
	loadTimeout  = 10 * time.Minute
	checkTimeout = 30 * time.Second
)

func post(ctx context.Context, c *http.Client, url string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

func getJSON(ctx context.Context, c *http.Client, url, apiKey string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func responseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}
