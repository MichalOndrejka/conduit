// Request-origin guard. Conduit has no authentication, so it relies on being
// reachable only from the local machine — but a web page open in a local
// browser can still reach 127.0.0.1. LocalOnly closes the two ways such a
// page could use that: DNS rebinding (reading responses under the attacker's
// own hostname) and cross-site form/fetch POSTs (CSRF).
package web

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// LocalOnly wraps next so that it only serves requests addressed to a
// loopback hostname (or one of extraHosts), and rejects state-changing
// requests a browser sent from another site. Non-browser clients (MCP
// clients, curl, the container healthcheck) send no Origin and pass.
func LocalOnly(next http.Handler, extraHosts ...string) http.Handler {
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, h := range extraHosts {
		if h = normalizeHost(h); h != "" {
			allowed[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DNS rebinding: the browser sends the attacker's hostname in Host,
		// even though it resolved to 127.0.0.1.
		if !allowed[normalizeHost(r.Host)] {
			http.Error(w, "forbidden: unexpected Host header", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			// CSRF: browsers attach Origin to every cross-origin request and
			// to same-origin POSTs; "null" (sandboxed iframe, file://) is
			// never one of ours.
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !allowed[normalizeHost(u.Host)] {
					http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// normalizeHost strips the port and IPv6 brackets and lowercases, so
// "LOCALHOST:8000", "[::1]:8000" and "::1" compare equal to their bare forms.
func normalizeHost(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(host)
}
