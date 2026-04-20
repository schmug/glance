package glance

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCommitterFromRequest_PrefersVerifiedContextOverHeaders(t *testing.T) {
	req := httptest.NewRequest("PUT", "/admin/api/files/x", nil)
	req.Header.Set("Cf-Access-Email", "attacker@evil.example")
	req.Header.Set("Cf-Access-Name", "Mallory")
	req = req.WithContext(withVerifiedEmail(req.Context(), "real@example.com"))

	got := committerFromRequest(req)
	if got.Email != "real@example.com" {
		t.Fatalf("email: got %q, want verified value (not the spoofed header)", got.Email)
	}
	if got.Name != "real" {
		t.Fatalf("name: got %q, want %q (derived from verified email, not the spoofed header)", got.Name, "real")
	}
}

func TestCommitterFromRequest_FallsBackToHeadersWhenNoContext(t *testing.T) {
	req := httptest.NewRequest("PUT", "/admin/api/files/x", nil)
	req.Header.Set("Cf-Access-Email", "dev@example.com")
	req.Header.Set("Cf-Access-Name", "Dev User")

	got := committerFromRequest(req)
	if got.Email != "dev@example.com" {
		t.Fatalf("email: got %q, want %q", got.Email, "dev@example.com")
	}
	if got.Name != "Dev User" {
		t.Fatalf("name: got %q, want %q", got.Name, "Dev User")
	}
}

func TestCommitterFromRequest_DefaultsWhenNothingProvided(t *testing.T) {
	req := httptest.NewRequest("PUT", "/admin/api/files/x", nil)

	got := committerFromRequest(req)
	if got.Email != "admin@glance.local" {
		t.Fatalf("email: got %q, want default", got.Email)
	}
	if got.Name != "admin" {
		t.Fatalf("name: got %q, want %q", got.Name, "admin")
	}
}

func TestAdminMiddleware_StashesVerifiedEmailInContext(t *testing.T) {
	a, _, signer, aud := newTestAdminServer(t)
	a.liveApp = func() *application { return &application{} }

	var seenEmail string
	var seenOK bool
	handler := a.middleware(func(w http.ResponseWriter, r *http.Request) {
		seenEmail, seenOK = verifiedEmailFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/admin/anywhere", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", mintToken(t, signer, aud, "you@example.com", time.Now().Add(time.Hour)))
	rw := httptest.NewRecorder()
	handler(rw, req)

	if rw.Code == http.StatusUnauthorized && rw.Header().Get("X-Admin-Auth-Failed") != "session" {
		t.Fatalf("CF Access layer rejected the request: %d / %s", rw.Code, rw.Header().Get("X-Admin-Auth-Failed"))
	}
	if !seenOK {
		t.Fatalf("verified email not found in request context")
	}
	if seenEmail != "you@example.com" {
		t.Fatalf("verified email: got %q, want %q", seenEmail, "you@example.com")
	}
}
