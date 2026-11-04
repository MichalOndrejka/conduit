package stats

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type ctxKey int

const (
	toolCallKey ctxKey = iota
	requestCounterKey
)

// ToolCallFrom returns the call being recorded for ctx, or nil when stats
// are off. Tool handlers fill in what only they know (query, filters,
// result count).
func ToolCallFrom(ctx context.Context) *ToolCall {
	tc, _ := ctx.Value(toolCallKey).(*ToolCall)
	return tc
}

// ToolMiddleware records every MCP tool call: name, mode, latency and
// error, plus whatever the handler added through ToolCallFrom.
func (r *Recorder) ToolMiddleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	if r == nil {
		return next
	}
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tc := &ToolCall{Name: req.Params.Name, Mode: requestMode(req.GetArguments())}
		if tc.Mode == "semantic_search" || tc.Name == "experience_retrieve" {
			tc.Cold = r.firstEmbedding()
		}
		start := time.Now()
		res, err := next(context.WithValue(ctx, toolCallKey, tc), req)
		tc.LatencyMs = time.Since(start).Milliseconds()
		switch {
		case err != nil:
			tc.Error = err.Error()
		case res != nil && res.IsError:
			tc.Error = resultText(res)
		}
		r.RecordTool(tc)
		return res, err
	}
}

// requestMode is a tool's request.mode (search tools, sync_source, …) or
// top-level mode argument.
func requestMode(args map[string]any) string {
	if req, ok := args["request"].(map[string]any); ok {
		if m, ok := req["mode"].(string); ok {
			return m
		}
	}
	m, _ := args["mode"].(string)
	return m
}

func resultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			if len(t.Text) > 500 {
				return t.Text[:500]
			}
			return t.Text
		}
	}
	return "error"
}

// RequestCounter counts a sync's HTTP requests to its source.
type RequestCounter struct {
	requests, rateLimited atomic.Int64
}

// Requests and RateLimited read the counts.
func (c *RequestCounter) Requests() int    { return int(c.requests.Load()) }
func (c *RequestCounter) RateLimited() int { return int(c.rateLimited.Load()) }

// WithRequestCounter returns a ctx under which HTTP requests made through a
// CountingTransport are counted.
func WithRequestCounter(ctx context.Context) (context.Context, *RequestCounter) {
	c := &RequestCounter{}
	return context.WithValue(ctx, requestCounterKey, c), c
}

// CountingTransport wraps base (nil: http.DefaultTransport) to count each
// request made under a WithRequestCounter context, and each response that
// is an API refusing the request for its rate limit.
func CountingTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return countingTransport{base}
}

type countingTransport struct{ base http.RoundTripper }

func (t countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if c, ok := req.Context().Value(requestCounterKey).(*RequestCounter); ok {
		c.requests.Add(1)
		if resp != nil && IsRateLimited(resp) {
			c.rateLimited.Add(1)
		}
	}
	return resp, err
}

// IsRateLimited reports whether resp is a rate-limit refusal: HTTP 429, or
// GitHub's 403 with no requests remaining or a Retry-After.
func IsRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
			return true
		}
	}
	return false
}
