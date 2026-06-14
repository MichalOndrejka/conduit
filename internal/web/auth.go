// Session + auth middleware — n8n-style JWT cookie auth.
//
// A successful login issues a signed JWT in the "conduit-auth" HttpOnly cookie
// (n8n uses "n8n-auth"). Requests pass if they carry a valid session cookie or
// a valid `Authorization: Bearer <CONDUIT_API_KEY>` header — the latter keeps
// headless MCP/API clients working, mirroring the Python ApiKeyAuthMiddleware
// in app/web/auth.py.
package web

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	sessionCookie  = "conduit-auth"
	jwtSecretFile  = ".jwt_secret"
	sessionTTL     = 7 * 24 * time.Hour // n8n default: 7 days
	renewThreshold = sessionTTL / 2     // sliding renewal once half-expired
)

type sessionClaims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

type AuthService struct {
	owners       *OwnerStore
	jwtSecret    []byte
	apiKey       string // CONDUIT_API_KEY for headless Bearer clients ("" = disabled)
	secureCookie bool
	limiter      *loginLimiter
}

func NewAuthService(dataDir string, owners *OwnerStore) (*AuthService, error) {
	secret, err := loadJWTSecret(dataDir)
	if err != nil {
		return nil, err
	}
	// Secure cookies default on (the demo sits behind Caddy TLS); allow
	// opting out for plain-HTTP local runs, mirroring n8n's N8N_SECURE_COOKIE.
	secure := true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("CONDUIT_SECURE_COOKIE"))); v == "0" || v == "false" || v == "no" {
		secure = false
	}
	return &AuthService{
		owners:       owners,
		jwtSecret:    secret,
		apiKey:       os.Getenv("CONDUIT_API_KEY"),
		secureCookie: secure,
		limiter:      newLoginLimiter(),
	}, nil
}

func loadJWTSecret(dataDir string) ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("CONDUIT_JWT_SECRET")); env != "" {
		return []byte(env), nil
	}
	path := filepath.Join(dataDir, jwtSecretFile)
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	// Generate and persist, like the Python .secret_key bootstrap.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return nil, err
	}
	return []byte(secret), nil
}

// ── JWT issue / verify ──────────────────────────────────────────────────────

func (a *AuthService) issueToken(email string, now time.Time) (string, error) {
	claims := sessionClaims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "conduit",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(sessionTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.jwtSecret)
}

func (a *AuthService) verifyToken(token string) (*sessionClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &sessionClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return a.jwtSecret, nil
	})
	if err != nil || !parsed.Valid {
		return nil, errors.New("invalid session")
	}
	claims, ok := parsed.Claims.(*sessionClaims)
	if !ok {
		return nil, errors.New("invalid session claims")
	}
	return claims, nil
}

// ── Cookie helpers ──────────────────────────────────────────────────────────

func (a *AuthService) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *AuthService) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

// ── Bearer API-key path (headless MCP/API clients) ──────────────────────────

func (a *AuthService) bearerAuthorized(r *http.Request) bool {
	if a.apiKey == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(auth, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return false
	}
	return hmac.Equal([]byte(strings.TrimSpace(value)), []byte(a.apiKey))
}

// sessionAuthorized validates the cookie and transparently renews it once it
// is past the renewal threshold (sliding session).
func (a *AuthService) sessionAuthorized(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	claims, err := a.verifyToken(cookie.Value)
	if err != nil {
		return false
	}
	if claims.ExpiresAt != nil && time.Until(claims.ExpiresAt.Time) < renewThreshold {
		if token, err := a.issueToken(claims.Email, time.Now()); err == nil {
			a.setSessionCookie(w, token)
		}
	}
	return true
}

// ── Middleware ──────────────────────────────────────────────────────────────

// publicPaths need no auth: login/setup pages, health probe, favicons.
var publicPaths = map[string]bool{
	"/login":       true,
	"/setup":       true,
	"/health":      true,
	"/favicon.svg": true,
	"/favicon.ico": true,
}

// Middleware enforces auth on every route except publicPaths. Browser
// requests without a session are redirected to /login; API/MCP requests get
// a 401 JSON body.
func (a *AuthService) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets (the stylesheet) must load on the unauthenticated
		// login/setup pages too, so the whole /static/ prefix is public.
		if publicPaths[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if a.bearerAuthorized(r) || a.sessionAuthorized(w, r) {
			next.ServeHTTP(w, r)
			return
		}
		// No owner provisioned yet → guide the browser to first-run setup.
		if !a.owners.HasOwner() && acceptsHTML(r) {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		if acceptsHTML(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
	})
}

func acceptsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// ── Login rate limiting ─────────────────────────────────────────────────────

// loginLimiter is a small in-memory token bucket per client IP: 5 attempts,
// refilling one every 30 seconds.
type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	limiterBurst  = 5.0
	limiterRefill = 30 * time.Second
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{buckets: map[string]*bucket{}}
}

func (l *loginLimiter) allow(remoteAddr string) bool {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		ip = remoteAddr
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: limiterBurst, last: now}
		l.buckets[ip] = b
	}
	b.tokens = min(limiterBurst, b.tokens+now.Sub(b.last).Seconds()/limiterRefill.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	// Opportunistic cleanup so the map can't grow unbounded.
	if len(l.buckets) > 10_000 {
		for k, v := range l.buckets {
			if now.Sub(v.last) > time.Hour {
				delete(l.buckets, k)
			}
		}
	}
	return true
}
