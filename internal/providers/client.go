package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/MichalOndrejka/conduit/internal/rag"
)

const (
	// MaxResponseBytes bounds a single response. A response over it is an
	// error, never silently truncated.
	MaxResponseBytes = 64 << 20

	maxFetchAttempts = 4
	maxRetryAfter    = 30 * time.Second
)

// RetryBaseDelay is the first retry's backoff, doubled per attempt. A var so
// tests can shrink it.
var RetryBaseDelay = time.Second

// errRetryable marks a failure worth retrying (network error, 429, 5xx);
// anything else (404, 401, …) fails immediately.
var errRetryable = errors.New("retryable")

// Client is an authenticated HTTP client with retries, shared by providers.
type Client struct {
	HTTP *http.Client
	// Authorize sets the request's credentials.
	Authorize func(*http.Request)
	// Header holds static headers sent with every request (e.g. Accept).
	Header http.Header
	// RetryAfter, if set, inspects a non-2xx response and reports whether it
	// is a transient refusal worth retrying and how long to wait (0 = the
	// default backoff) — for provider-specific signals such as GitHub's
	// rate-limit 403s. 429 and 5xx are always retried.
	RetryAfter func(resp *http.Response) (retry bool, wait time.Duration)
}

// Get performs a GET and returns the raw body.
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	data, _, err := c.Do(ctx, http.MethodGet, url, nil)
	return data, err
}

// GetJSON performs a GET and decodes the JSON body into v, returning the
// response headers (for header-based paging).
func (c *Client) GetJSON(ctx context.Context, url string, v any) (http.Header, error) {
	data, header, err := c.Do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if err := decodeJSON(data, url, v); err != nil {
		return nil, err
	}
	return header, nil
}

// PostJSON POSTs body as JSON and decodes the JSON response into v.
func (c *Client) PostJSON(ctx context.Context, url string, body, v any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	data, _, err := c.Do(ctx, http.MethodPost, url, payload)
	if err != nil {
		return err
	}
	return decodeJSON(data, url, v)
}

func decodeJSON(data []byte, url string, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		// A sign-in page instead of JSON is what some servers (Azure DevOps
		// among them) answer a rejected token with.
		return fmt.Errorf("response from %s is not valid JSON (is the token valid?): %w", url, err)
	}
	return nil
}

// Do performs a request, retrying transient failures with backoff and
// honouring Retry-After.
func (c *Client) Do(ctx context.Context, method, url string, body []byte) ([]byte, http.Header, error) {
	var lastErr error
	for attempt := 0; attempt < maxFetchAttempts; attempt++ {
		data, header, wait, err := c.once(ctx, method, url, body)
		if err == nil {
			return data, header, nil
		}
		if !errors.Is(err, errRetryable) || ctx.Err() != nil {
			return nil, nil, err
		}
		lastErr = err
		if attempt == maxFetchAttempts-1 {
			break
		}
		delay := RetryBaseDelay << attempt
		if wait > 0 {
			delay = wait
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return nil, nil, fmt.Errorf("giving up after %d attempts: %w", maxFetchAttempts, lastErr)
}

func (c *Client) once(ctx context.Context, method, url string, body []byte) (data []byte, header http.Header, wait time.Duration, err error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, nil, 0, err
	}
	for k, vs := range c.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Authorize != nil {
		c.Authorize(req)
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, 0, ctx.Err()
		}
		return nil, nil, 0, fmt.Errorf("%w: %v", errRetryable, err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%w: reading %s: %v", errRetryable, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		if !retry && c.RetryAfter != nil {
			var w time.Duration
			if retry, w = c.RetryAfter(resp); w > 0 {
				wait = w
			}
		}
		wait = min(wait, maxRetryAfter)
		msg := &StatusError{Code: resp.StatusCode, msg: fmt.Sprintf("HTTP %d from %s: %s", resp.StatusCode, url, rag.Truncate(string(data), 300))}
		if retry {
			return nil, nil, wait, fmt.Errorf("%w: %v", errRetryable, msg)
		}
		return nil, nil, 0, msg
	}
	if len(data) > MaxResponseBytes {
		return nil, nil, 0, fmt.Errorf("response from %s exceeds %d MB", url, MaxResponseBytes>>20)
	}
	return data, resp.Header, 0, nil
}

// StatusError is a non-2xx response that wasn't retried.
type StatusError struct {
	Code int
	msg  string
}

func (e *StatusError) Error() string { return e.msg }

// IsStatus reports whether err is a StatusError with the given code.
func IsStatus(err error, code int) bool {
	var s *StatusError
	return errors.As(err, &s) && s.Code == code
}
