// Conduit (Go) — entrypoint. Port of app/main.py's lifespan wiring: config,
// secrets, RAG services, MCP server, web routes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/embedstate"
	"github.com/MichalOndrejka/conduit/internal/health"
	"github.com/MichalOndrejka/conduit/internal/mcptools"
	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/setup"
	"github.com/MichalOndrejka/conduit/internal/sourcemgmt"
	"github.com/MichalOndrejka/conduit/internal/store"
	"github.com/MichalOndrejka/conduit/internal/syncctl"
	"github.com/MichalOndrejka/conduit/internal/syncsvc"
	"github.com/MichalOndrejka/conduit/internal/web"
)

func main() {
	// Container HEALTHCHECK probe (see Dockerfile): the distroless image has
	// no shell or curl, so the binary checks its own /health endpoint.
	// Dispatched before any setup so a probe never starts health monitors or
	// touches the data dir.
	//   conduit healthcheck
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// docker-compose's one-shot step before Conduit starts: pull models and
	// check GPU placement (see internal/setup). Needs only the config.
	//   conduit setup
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		if err := runSetup(cfg); err != nil {
			os.Exit(1)
		}
		return
	}
	dataDir := config.DataDir(cfg)

	secretsStore, err := secrets.New(dataDir)
	if err != nil {
		log.Fatalf("secrets store: %v", err)
	}

	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg)
	sourceStore := store.NewSourceConfigStore(cfg.SourcesFilePath)
	searchSvc := rag.NewSearchService(vectors, embedding, sourceStore)
	memorySvc := memory.NewService(vectors, embedding)

	// Reset any source left in "syncing" by a previous crash or container restart.
	if err := sourceStore.ReconcileStaleSync(); err != nil {
		log.Printf("warning: could not reconcile stale sync state: %v", err)
	}

	// An embedding model change made outside Settings (env var, edited
	// config.json) makes existing vectors incompatible; drop them now rather
	// than letting new syncs mix two models in one collection.
	reconcileCtx, cancelReconcile := context.WithTimeout(context.Background(), 30*time.Second)
	changed, prev, err := embedstate.Reconcile(reconcileCtx, dataDir, cfg.Embedding, vectors, sourceStore)
	cancelReconcile()
	switch {
	case changed && err != nil:
		log.Printf("WARNING: embedding model changed (%s/%d → %s/%d) but dropping old vectors failed: %v — "+
			"all sources are flagged for reindex; will retry on next start",
			prev.Model, prev.Dimensions, cfg.Embedding.Model, cfg.Embedding.Dimensions, err)
	case changed:
		log.Printf("WARNING: embedding model changed (%s/%d → %s/%d): dropped all collections, "+
			"including experience memories — re-sync every source",
			prev.Model, prev.Dimensions, cfg.Embedding.Model, cfg.Embedding.Dimensions)
	case err != nil:
		log.Printf("warning: could not check embedding model state: %v", err)
	}

	// Create every collection before the tools are served, so none of them
	// can hit a missing one. If Qdrant isn't reachable yet, the health probe
	// below creates them once it is.
	ensureCtx, cancelEnsure := context.WithTimeout(context.Background(), 30*time.Second)
	if err := vectors.EnsureCollections(ensureCtx, models.AllCollections, cfg.Embedding.Dimensions); err != nil {
		log.Printf("warning: could not create collections yet (retried by the Qdrant health probe): %v", err)
	}
	cancelEnsure()

	// ── Sync engine (generic sources: API + manual) ────────────────────────
	chunker := rag.NewTextChunker(cfg)
	indexer := rag.NewDocumentIndexer(vectors, embedding, chunker)
	syncSvc := syncsvc.New(
		cfg, sourceStore, secretsStore, indexer,
		syncctl.NewProgressStore(), syncctl.NewControlStore(),
	)

	// ── Health probes (background, exponential backoff) ────────────────────
	healthMon := health.Start(cfg, vectors, embedding)

	// Verification subcommand (plan §Verification): embeds a query and
	// searches an existing collection, for parity checks against Python.
	//   conduit search <collection> <query...>
	if len(os.Args) > 1 && os.Args[1] == "search" {
		runSearchCLI(searchSvc, os.Args[2:])
		return
	}

	// ── MCP server (streamable HTTP at /mcp) ───────────────────────────────
	mcpServer := server.NewMCPServer("Conduit", version)
	mcptools.RegisterTools(mcpServer, searchSvc, memorySvc)
	mcptools.RegisterSyncTool(mcpServer, sourceStore, syncSvc)
	mcptools.RegisterSourcesTool(mcpServer, sourcemgmt.New(sourceStore, vectors, secretsStore), syncSvc)
	mcpHTTP := server.NewStreamableHTTPServer(mcpServer, server.WithEndpointPath("/mcp"))

	// ── HTTP wiring ─────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	webServer := web.NewServer(cfg, sourceStore, vectors, memorySvc, secretsStore, syncSvc, healthMon)
	webServer.Routes(mux)
	mux.Handle("/mcp", mcpHTTP)

	// Docker sends SIGTERM on `docker stop` (SIGINT covers Ctrl+C locally).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Every route, /mcp included, rejects DNS-rebinding and cross-site requests.
	// An explicit CONDUIT_HOST bind address is also accepted as a Host value.
	srv := &http.Server{
		Addr:              listenAddr(),
		Handler:           web.LocalOnly(mux, os.Getenv("CONDUIT_HOST")),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Printf("Conduit (Go) listening on %s — qdrant=%s embedding=%s",
		srv.Addr, cfg.Qdrant.URL, cfg.Embedding.Model)

	select {
	case err := <-serveErr:
		log.Fatal(err)
	case <-ctx.Done():
	}
	stop() // a second signal now kills the process immediately
	shutdown(srv, syncSvc)
}

const version = "0.1.0"

// shutdownTimeout keeps the whole graceful stop inside Docker's default
// 10 s grace period before it escalates to SIGKILL.
const shutdownTimeout = 8 * time.Second

// shutdown stops accepting requests, then cancels running syncs and waits
// for them to record their final status (idle) to conduit-sources.json.
func shutdown(srv *http.Server, syncSvc *syncsvc.Service) {
	log.Printf("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Shutdown waits for in-flight requests, but MCP streaming connections
	// can stay open indefinitely, so give it only part of the budget and
	// then force-close whatever is left.
	httpCtx, httpCancel := context.WithTimeout(ctx, shutdownTimeout/2)
	defer httpCancel()
	if err := srv.Shutdown(httpCtx); err != nil {
		log.Printf("warning: HTTP shutdown: %v — closing remaining connections", err)
		_ = srv.Close()
	}
	if err := syncSvc.Shutdown(ctx); err != nil {
		log.Printf("warning: running syncs did not stop in time: %v", err)
	}
	log.Printf("stopped")
}

// listenAddr resolves the bind address from CONDUIT_HOST and PORT. Outside a
// container it defaults to loopback so a local `go run` doesn't trigger the
// Windows "allow this app on public/private networks" firewall prompt, which
// only fires for listeners reachable from other interfaces. Inside one it
// defaults to 0.0.0.0, since Docker's port mapping can't reach loopback.
func listenAddr() string {
	host := os.Getenv("CONDUIT_HOST")
	if host == "" {
		host = "127.0.0.1"
		if config.InContainer() {
			host = "0.0.0.0"
		}
	}
	port := "8000"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}
	return net.JoinHostPort(host, port)
}

// runSetup prepares the configured embedding and preprocessing models.
// OLLAMA_GPU_EXPECTED is set by the GPU compose overlays.
func runSetup(cfg *config.AppConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ms := []setup.Model{{
		Name: cfg.Embedding.Model, OllamaURL: setup.OllamaURL(cfg.Embedding.BaseURL), Embedding: true,
	}}
	if cfg.Preprocessing.Model != "" {
		ms = append(ms, setup.Model{
			Name: cfg.Preprocessing.Model, OllamaURL: setup.OllamaURL(cfg.Preprocessing.BaseURL),
		})
	}
	return setup.Run(ctx, setup.Options{
		QdrantURL:    strings.TrimRight(cfg.Qdrant.URL, "/"),
		QdrantAPIKey: cfg.Qdrant.APIKey,
		Models:       ms,
		GPUExpected:  os.Getenv("OLLAMA_GPU_EXPECTED"),
		Out:          os.Stdout,
	})
}

// runHealthcheck reports whether this process's own web server answers
// /health. It checks liveness only: Qdrant/Ollama being down shows up in the
// response body but is not a failure, since restarting Conduit can't fix them.
func runHealthcheck() error {
	host, port, err := net.SplitHostPort(listenAddr())
	if err != nil {
		return err
	}
	// A wildcard bind address isn't dialable; the server is reachable on loopback.
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/health returned %s", resp.Status)
	}
	return nil
}

func runSearchCLI(searchSvc *rag.SearchService, args []string) {
	if len(args) < 2 {
		log.Fatal("usage: conduit search <collection> <query...>")
	}
	collection := args[0]
	query := strings.Join(args[1:], " ")
	results, hasMore, err := searchSvc.Search(context.Background(), collection, query, 1, nil)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	out, err := json.MarshalIndent(map[string]any{"results": results, "has_more": hasMore}, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(out))
}
