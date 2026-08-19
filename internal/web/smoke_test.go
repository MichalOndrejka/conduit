package web

import (
	"net/http"
	"testing"
)

func TestHarnessSmoke(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200; body: %s", resp.StatusCode, bodyString(t, resp))
	}
}
