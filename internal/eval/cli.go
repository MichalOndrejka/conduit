package eval

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/store"
)

const usage = `Usage: conduit eval <command> [flags]

Commands:
  retrieval  Index the dataset under every layout and score each layout's ideal search (no LLM, free).
  agent      Let Claude answer every question with each layout's tools and score its answers (Claude API, costs money).
  export     Fetch the configured sources into a private dataset for labelling your own questions.
  find       Search a dataset's index to look up document IDs while labelling questions.

Run "conduit eval <command> -h" for a command's flags.
`

// Run is the "conduit eval" command.
func Run(ctx context.Context, cfg *config.AppConfig, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("missing command")
	}
	switch args[0] {
	case "retrieval":
		return runRetrieval(ctx, cfg, args[1:], stdout, stderr)
	case "agent":
		return runAgentCmd(ctx, cfg, args[1:], stdout, stderr)
	case "export":
		return runExport(ctx, cfg, args[1:], stdout, stderr)
	case "find":
		return runFind(ctx, cfg, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

// common are the flags every run shares.
type common struct {
	dataset, queries, companies, queryIDs, out string
	reuse                                      bool
	k                                          int
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.dataset, "dataset", BuiltinSynthetic, `dataset JSON file, or "synthetic" for the committed one`)
	fs.StringVar(&c.queries, "queries", "", "extra questions file (QueriesFile JSON), e.g. labelled questions for an exported dataset")
	fs.StringVar(&c.companies, "companies", "", "comma-separated company IDs to run (default: all)")
	fs.StringVar(&c.queryIDs, "query-ids", "", "comma-separated question IDs to run (default: all)")
	fs.StringVar(&c.out, "out", "eval-results", "directory for the JSON and Markdown reports")
	fs.BoolVar(&c.reuse, "reuse", false, "reuse already-indexed eval collections instead of re-embedding the corpus")
	fs.IntVar(&c.k, "k", 10, "documents scored per question")
}

func (c *common) load() (*Dataset, error) {
	ds, err := LoadDataset(c.dataset, c.queries)
	if err != nil {
		return nil, err
	}
	ds.Filter(splitList(c.companies), splitList(c.queryIDs))
	n := 0
	for _, co := range ds.Companies {
		n += len(co.Queries)
	}
	if n == 0 {
		return nil, fmt.Errorf("no questions to run (an exported dataset needs a -queries file)")
	}
	return ds, nil
}

func (c *common) runner(ctx context.Context, cfg *config.AppConfig, ds *Dataset, stderr io.Writer) (*Runner, error) {
	backend := NewQdrantBackend(cfg)
	if err := backend.Check(ctx); err != nil {
		return nil, fmt.Errorf("the eval needs Qdrant and the embedding model configured for Conduit: %w", err)
	}
	return &Runner{Backend: backend, Dataset: ds, Reuse: c.reuse, K: c.k, Log: stderr}, nil
}

func layoutNames(raw string, def []string) ([]string, error) {
	names := splitList(raw)
	if len(names) == 0 {
		names = def
	}
	for _, n := range names {
		if _, ok := LayoutByName(n); !ok {
			return nil, fmt.Errorf("unknown layout %q", n)
		}
	}
	return names, nil
}

func indexesOf(layouts []string) []string {
	var out []string
	for _, n := range layouts {
		l, _ := LayoutByName(n)
		if !slices.Contains(out, l.Index) {
			out = append(out, l.Index)
		}
	}
	return out
}

func runRetrieval(ctx context.Context, cfg *config.AppConfig, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("eval retrieval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	allNames := make([]string, len(Layouts))
	for i, l := range Layouts {
		allNames[i] = l.Name
	}
	layoutsFlag := fs.String("layouts", "", "comma-separated layouts, baseline first (default: "+strings.Join(allNames, ",")+")")
	cleanup := fs.Bool("cleanup", false, "delete the eval collections afterwards")
	if err := fs.Parse(args); err != nil {
		return err
	}
	names, err := layoutNames(*layoutsFlag, allNames)
	if err != nil {
		return err
	}
	ds, err := c.load()
	if err != nil {
		return err
	}
	r, err := c.runner(ctx, cfg, ds, stderr)
	if err != nil {
		return err
	}
	if err := r.Prepare(ctx, indexesOf(names)); err != nil {
		return err
	}
	layouts := make([]Layout, len(names))
	for i, n := range names {
		layouts[i], _ = LayoutByName(n)
	}
	results, err := r.RunRetrieval(ctx, layouts)
	if err != nil {
		return err
	}
	rep := NewReport("retrieval", ds, r.k(), names, results, map[string]string{"Embedding model": cfg.Embedding.Model})
	path, err := rep.Write(c.out)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, rep.Markdown())
	fmt.Fprintf(stderr, "report written to %s (and .json)\n", path)
	if *cleanup {
		return r.Cleanup(ctx)
	}
	return nil
}

func runAgentCmd(ctx context.Context, cfg *config.AppConfig, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("eval agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	layoutsFlag := fs.String("layouts", "", "comma-separated agent layouts, baseline first (default: "+strings.Join(AgentLayouts, ",")+")")
	var opts AgentOptions
	fs.StringVar(&opts.Model, "model", DefaultAgentModel, "Claude model")
	fs.StringVar(&opts.Effort, "effort", "medium", "effort: low, medium, high, xhigh or max")
	fs.IntVar(&opts.MaxToolCalls, "max-tool-calls", 8, "tool calls allowed per question")
	fs.IntVar(&opts.Concurrency, "concurrency", 4, "questions answered in parallel")
	yes := fs.Bool("yes", false, "run without stopping at the cost confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	names, err := layoutNames(*layoutsFlag, AgentLayouts)
	if err != nil {
		return err
	}
	for _, n := range names {
		if !slices.Contains(AgentLayouts, n) {
			return fmt.Errorf("layout %q has no agent tools (agent layouts: %s)", n, strings.Join(AgentLayouts, ", "))
		}
	}
	ds, err := c.load()
	if err != nil {
		return err
	}
	questions := 0
	for _, co := range ds.Companies {
		questions += len(co.Queries)
	}
	runs := questions * len(names)
	fmt.Fprintf(stderr, "agent eval: %d questions × %d layouts = %d agent runs on %s (effort %s), up to %d tool calls each.\n",
		questions, len(names), runs, opts.Model, opts.Effort, opts.MaxToolCalls)
	if !*yes {
		fmt.Fprintln(stderr, "This calls the Claude API and costs money. Re-run with -yes to start; try -query-ids or -companies for a small first run.")
		return nil
	}
	r, err := c.runner(ctx, cfg, ds, stderr)
	if err != nil {
		return err
	}
	if err := r.Prepare(ctx, indexesOf(names)); err != nil {
		return err
	}
	// Credentials resolve as the SDK does: ANTHROPIC_API_KEY, or an
	// "ant auth login" profile.
	client := anthropic.NewClient()
	results, err := r.RunAgent(ctx, client, names, opts)
	if err != nil {
		return err
	}
	var usage TokenUsage
	for _, res := range results {
		if res.Agent != nil {
			usage.Input += res.Agent.Usage.Input
			usage.Output += res.Agent.Usage.Output
			usage.CacheRead += res.Agent.Usage.CacheRead
			usage.CacheWrite += res.Agent.Usage.CacheWrite
		}
	}
	settings := map[string]string{
		"Model": opts.Model, "Effort": opts.Effort, "Max tool calls": fmt.Sprint(opts.MaxToolCalls),
		"Embedding model": cfg.Embedding.Model,
		"Tokens":          fmt.Sprintf("%d input, %d output, %d cache read, %d cache write", usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite),
	}
	rep := NewReport("agent", ds, r.k(), names, results, settings)
	path, err := rep.Write(c.out)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, rep.Markdown())
	fmt.Fprintf(stderr, "report written to %s (and .json, with every tool call)\n", path)
	return nil
}

func runExport(ctx context.Context, cfg *config.AppConfig, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("eval export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "eval-private/dataset.json", "where to write the dataset (keep it out of git)")
	company := fs.String("company", "real", "company ID in the dataset")
	sourceIDs := fs.String("sources", "", "comma-separated source IDs (default: every enabled source)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	secretsStore, err := secrets.New(config.DataDir(cfg))
	if err != nil {
		return err
	}
	all, err := store.NewSourceConfigStore(cfg.SourcesFilePath).ListAll()
	if err != nil {
		return err
	}
	ds, err := Export(ctx, all, secretsStore, ExportOptions{
		Company: *company, SourceIDs: splitList(*sourceIDs),
		Log: func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) },
	})
	if err != nil {
		return err
	}
	if err := WriteJSON(*out, ds); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s: %s\nNext: label questions in a queries file (look up document IDs with \"conduit eval find\"), then run \"conduit eval retrieval -dataset %s -queries <file>\".\n",
		*out, ds.Summary(), *out)
	return nil
}

func runFind(ctx context.Context, cfg *config.AppConfig, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("eval find", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	company := fs.String("company", "", "company ID (default: the dataset's first)")
	query := fs.String("q", "", "search text")
	n := fs.Int("n", 10, "documents to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *query == "" {
		return fmt.Errorf("-q is required")
	}
	ds, err := LoadDataset(c.dataset, c.queries)
	if err != nil {
		return err
	}
	if *company != "" {
		ds.Filter([]string{*company}, nil)
	}
	if len(ds.Companies) == 0 {
		return fmt.Errorf("no company %q in the dataset", *company)
	}
	co := &ds.Companies[0]
	r, err := c.runner(ctx, cfg, ds, stderr)
	if err != nil {
		return err
	}
	r.Reuse = true // looking things up shouldn't re-embed an indexed corpus
	if err := r.Prepare(ctx, []string{IndexMerged}); err != nil {
		return err
	}
	byID := map[string]Document{}
	for _, d := range co.Documents {
		byID[d.ID] = d
	}
	var targets []Target
	for _, coll := range mergedCollections {
		targets = append(targets, Target{Index: IndexMerged, Collection: coll})
	}
	docs, err := r.search(ctx, co.ID, *query, targets, *n)
	if err != nil {
		return err
	}
	for i, d := range docs {
		doc := byID[d.SourceDocID]
		fmt.Fprintf(stdout, "%2d. %.3f  %-14s %s\n    %s\n", i+1, d.Score, doc.Intent, d.SourceDocID, doc.Title)
	}
	return nil
}
