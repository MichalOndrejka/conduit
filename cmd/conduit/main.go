// Conduit (Go) — entrypoint. Port of app/main.py's lifespan wiring: config,
// secrets, RAG services, MCP server, web routes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/health"
	"github.com/MichalOndrejka/conduit/internal/mcptools"
	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/store"
	"github.com/MichalOndrejka/conduit/internal/syncctl"
	"github.com/MichalOndrejka/conduit/internal/syncsvc"
	"github.com/MichalOndrejka/conduit/internal/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	dataDir := config.DataDir(cfg)

	secretsStore, err := secrets.New(dataDir)
	if err != nil {
		log.Fatalf("secrets store: %v", err)
	}

	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg, secretsStore)
	sourceStore := store.NewSourceConfigStore(cfg.SourcesFilePath)
	searchSvc := rag.NewSearchService(vectors, embedding, sourceStore)
	memorySvc := memory.NewService(vectors, embedding)

	// Reset any source left in "syncing" by a previous crash or container restart.
	if err := sourceStore.ReconcileStaleSync(); err != nil {
		log.Printf("warning: could not reconcile stale sync state: %v", err)
	}

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
	mcpHTTP := server.NewStreamableHTTPServer(mcpServer, server.WithEndpointPath("/mcp"))

	// ── HTTP wiring ─────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	webServer := web.NewServer(cfg, sourceStore, vectors, memorySvc, secretsStore, syncSvc, healthMon)
	webServer.Routes(mux)
	mux.Handle("/mcp", mcpHTTP)

	// Bind to loopback by default so a local `go run` doesn't trigger the
	// Windows "allow this app on public/private networks" firewall prompt,
	// which only fires for listeners reachable from other interfaces.
	// Docker needs the container-internal 0.0.0.0 so the port mapping works —
	// set CONDUIT_HOST=0.0.0.0 there, e.g. `docker run -e CONDUIT_HOST=0.0.0.0 ...`.
	host := os.Getenv("CONDUIT_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := "8000"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}
	addr := host + ":" + port
	log.Printf("Conduit (Go) listening on %s — qdrant=%s:%d embedding=%s/%s",
		addr, cfg.Qdrant.Host, cfg.Qdrant.Port, cfg.Embedding.Provider, cfg.Embedding.Model)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

const version = "0.1.0"

func runSearchCLI(searchSvc *rag.SearchService, args []string) {
	if len(args) < 2 {
		log.Fatal("usage: conduit search <collection> <query...>")
	}
	collection := args[0]
	query := strings.Join(args[1:], " ")
	results, err := searchSvc.Search(context.Background(), collection, query, 5, nil)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(out))
}
