// Auth tests — Go counterpart of tests/test_auth_middleware.py, extended for
// the n8n-style JWT session flow.
package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestAuth(t *testing.T, apiKey string) (*AuthService, *OwnerStore) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CONDUIT_OWNER_EMAIL", "owner@example.com")
	t.Setenv("CONDUIT_OWNER_PASSWORD", "correct-horse-battery")
	t.Setenv("CONDUIT_API_KEY", apiKey)
	t.Setenv("CONDUIT_JWT_SECRET", "test-jwt-secret-test-jwt-secret!")
	t.Setenv("CONDUIT_SECURE_COOKIE", "false")
	owners, err := NewOwnerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuthService(dir, owners)
	if err != nil {
		t.Fatal(err)
	}
	return auth, owners
}

func protectedApp(auth *AuthService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	// Mirrors the Python mounted-sub-app test: /mcp must be gated too.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("mcp"))
	})
	return auth.Middleware(mux)
}

func TestMissingCredentialsRejected(t *testing.T) {
	auth, _ := newTestAuth(t, "secret")
	rec := httptest.NewRecorder()
	protectedApp(auth).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestBrowserRedirectedToLogin(t *testing.T) {
	auth, _ := newTestAuth(t, "")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	protectedApp(auth).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Errorf("status=%d location=%q, want 302 /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMCPGatedLikeMountedSubApp(t *testing.T) {
	auth, _ := newTestAuth(t, "secret")
	app := protectedApp(auth)

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /mcp: status = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest("GET", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("bearer /mcp: status = %d, want 200", rec.Code)
	}
}

func TestWrongBearerTokenRejected(t *testing.T) {
	auth, _ := newTestAuth(t, "secret")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	protectedApp(auth).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestBearerDisabledWhenNoAPIKey(t *testing.T) {
	auth, _ := newTestAuth(t, "")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	protectedApp(auth).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (empty API key must not allow bearer)", rec.Code)
	}
}

func TestSessionCookieFlow(t *testing.T) {
	auth, owners := newTestAuth(t, "")
	if !owners.Verify("owner@example.com", "correct-horse-battery") {
		t.Fatal("owner password verify failed")
	}
	token, err := auth.issueToken("owner@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	protectedApp(auth).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid session: status = %d, want 200", rec.Code)
	}

	// Tampered JWT must be rejected.
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token + "x"})
	rec = httptest.NewRecorder()
	protectedApp(auth).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered session: status = %d, want 401", rec.Code)
	}
}

func TestWrongPasswordRejected(t *testing.T) {
	_, owners := newTestAuth(t, "")
	if owners.Verify("owner@example.com", "wrong-password") {
		t.Error("wrong password accepted")
	}
	if owners.Verify("other@example.com", "correct-horse-battery") {
		t.Error("wrong email accepted")
	}
}

func TestPublicPathsOpen(t *testing.T) {
	auth, _ := newTestAuth(t, "secret")
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("healthy"))
	})
	app := auth.Middleware(mux)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/health: status = %d, want 200 without auth", rec.Code)
	}
}

func TestLoginRateLimiter(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < int(limiterBurst); i++ {
		if !l.allow("203.0.113.7:1234") {
			t.Fatalf("attempt %d unexpectedly blocked", i+1)
		}
	}
	if l.allow("203.0.113.7:1234") {
		t.Error("burst exceeded but attempt allowed")
	}
	if !l.allow("198.51.100.9:4321") {
		t.Error("different IP should not be affected")
	}
}

func TestFirstRunSetup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_OWNER_EMAIL", "")
	t.Setenv("CONDUIT_OWNER_PASSWORD", "")
	owners, err := NewOwnerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if owners.HasOwner() {
		t.Fatal("fresh store should have no owner")
	}
	if err := owners.Setup("bad-email", "longenough"); err == nil {
		t.Error("invalid email accepted")
	}
	if err := owners.Setup("a@b.com", "short"); err == nil {
		t.Error("short password accepted")
	}
	if err := owners.Setup("owner@example.com", "longenough"); err != nil {
		t.Fatal(err)
	}
	if err := owners.Setup("again@example.com", "longenough"); err == nil {
		t.Error("second setup accepted")
	}

	// Owner persists across restarts.
	owners2, err := NewOwnerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !owners2.Verify("owner@example.com", "longenough") {
		t.Error("persisted owner does not verify")
	}
}
