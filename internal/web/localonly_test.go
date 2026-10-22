package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalOnly(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := LocalOnly(ok, "conduit.lan")

	cases := []struct {
		name    string
		method  string
		host    string
		headers map[string]string
		want    int
	}{
		{"loopback name", "GET", "localhost:8000", nil, 200},
		{"loopback IPv4", "GET", "127.0.0.1:8000", nil, 200},
		{"loopback IPv6", "GET", "[::1]:8000", nil, 200},
		{"host without port", "GET", "localhost", nil, 200},
		{"host case-insensitive", "GET", "LocalHost:8000", nil, 200},
		{"extra host", "GET", "conduit.lan:8000", nil, 200},
		{"rebinding hostname", "GET", "evil.example:8000", nil, 403},
		{"rebinding on MCP POST", "POST", "evil.example:8000", nil, 403},
		{"POST without Origin (MCP client, curl)", "POST", "127.0.0.1:8000", nil, 200},
		{"POST same-origin", "POST", "localhost:8000",
			map[string]string{"Origin": "http://localhost:8000", "Sec-Fetch-Site": "same-origin"}, 200},
		{"POST from other local port", "POST", "localhost:8000",
			map[string]string{"Origin": "http://127.0.0.1:6274"}, 200},
		{"POST cross-site Origin", "POST", "127.0.0.1:8000",
			map[string]string{"Origin": "https://evil.example"}, 403},
		{"POST null Origin", "POST", "127.0.0.1:8000",
			map[string]string{"Origin": "null"}, 403},
		{"POST cross-site fetch metadata only", "POST", "127.0.0.1:8000",
			map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"DELETE cross-site", "DELETE", "127.0.0.1:8000",
			map[string]string{"Origin": "https://evil.example"}, 403},
		{"GET cross-site navigation", "GET", "localhost:8000",
			map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "/", nil)
			req.Host = c.host
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}
