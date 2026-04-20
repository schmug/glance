# Web UI Config Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an authenticated `/admin` editor for `glance.yml` and `!include`d files with live skeleton preview, on-demand full render, git-backed history, and Cloudflare Access + Glance password defense-in-depth auth.

**Architecture:** All new code lives in `internal/glance/admin*.go`. The admin HTTP handler mounts on the same mux as the dashboard when three conditions are satisfied: `admin.enabled: true`, `auth.users` is non-empty, and (CF Access is configured OR `GLANCE_ADMIN_DEV_BYPASS=1`). Saves write to a mirrored git repo first (so history stays in sync) then to the real path, letting Glance's existing fsnotify watcher trigger the live reload.

**Tech Stack:** Go 1.24+ (stdlib + `gopkg.in/yaml.v3`), `github.com/coreos/go-oidc/v3` for JWT verification, system `git` (shelled out), vanilla JS using `createElement`/`textContent` (no `innerHTML` with dynamic data), vendored `js-yaml`, Go `html/template`.

**Design spec:** `docs/superpowers/specs/2026-04-19-webui-config-editor-design.md`

**Branch:** `feat/admin-editor` (already checked out)

---

## File Structure

**New files:**
- `internal/glance/admin.go` — admin server struct, routes, middleware composition, file API
- `internal/glance/admin_cfaccess.go` — Cloudflare Access JWT verification with JWKS caching
- `internal/glance/admin_history.go` — thin wrapper over system `git` for the history repo
- `internal/glance/admin_preview.go` — preview app registry, TTL/LRU eviction, iframe-prefix router
- `internal/glance/admin_widget_stubs.go` — widget-type → YAML-stub map, compile-time
- `internal/glance/admin_test.go` — table-driven tests for admin handlers
- `internal/glance/admin_cfaccess_test.go` — JWT verifier unit tests
- `internal/glance/admin_history_test.go` — git wrapper tests using `t.TempDir()`
- `internal/glance/admin_preview_test.go` — registry unit tests
- `internal/glance/templates/admin.html` — editor shell with full DOM structure (no JS-side shell injection)
- `internal/glance/static/js/admin/app.js` — client code (editor, preview, history, save flow) using DOM APIs only
- `internal/glance/static/js/admin/js-yaml.min.js` — vendored YAML parser, ~40KB
- `internal/glance/static/css/admin.css` — admin-only styles

**Modified files:**
- `internal/glance/config.go` — add `admin:` block to the `config` struct
- `internal/glance/main.go` — expose live `*application` pointer to admin; wire admin mounting
- `internal/glance/glance.go:436` — in `server()`, call admin handler registration when enabled
- `go.mod` / `go.sum` — add `github.com/coreos/go-oidc/v3`
- `docs/configuration.md` — document the new `admin:` block

---

### Task 1: Add `admin:` config schema block

**Files:**
- Modify: `internal/glance/config.go` (extend the `config` struct around line 30)
- Create: `internal/glance/admin_test.go`

- [ ] **Step 1: Write a failing test that parses an admin YAML block**

Create `internal/glance/admin_test.go`:

```go
package glance

import (
	"strings"
	"testing"
)

func TestConfigParsesAdminBlock(t *testing.T) {
	yamlSrc := `
server:
  port: 8080
auth:
  secret-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
  users:
    cory:
      password: hunter2
admin:
  enabled: true
  history-dir: ./.glance-history
  cloudflare-access:
    team-domain: example.cloudflareaccess.com
    audience: abc123
    allowed-emails:
      - you@example.com
pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: clock
`
	cfg, err := newConfigFromYAML([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !cfg.Admin.Enabled {
		t.Fatal("expected Admin.Enabled true")
	}
	if cfg.Admin.HistoryDir != "./.glance-history" {
		t.Fatalf("history-dir: got %q", cfg.Admin.HistoryDir)
	}
	if cfg.Admin.CloudflareAccess.TeamDomain != "example.cloudflareaccess.com" {
		t.Fatalf("team-domain: got %q", cfg.Admin.CloudflareAccess.TeamDomain)
	}
	if cfg.Admin.CloudflareAccess.Audience != "abc123" {
		t.Fatalf("audience: got %q", cfg.Admin.CloudflareAccess.Audience)
	}
	if len(cfg.Admin.CloudflareAccess.AllowedEmails) != 1 ||
		!strings.EqualFold(cfg.Admin.CloudflareAccess.AllowedEmails[0], "you@example.com") {
		t.Fatalf("allowed-emails: got %v", cfg.Admin.CloudflareAccess.AllowedEmails)
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

Run: `go test ./internal/glance/ -run TestConfigParsesAdminBlock -v`
Expected: FAIL — `cfg.Admin` undefined.

- [ ] **Step 3: Add the `Admin` block to the `config` struct in `config.go`**

In `internal/glance/config.go`, find the `type config struct { ... }` block (around line 30) and add this field at the end of the struct, just before the closing brace:

```go
	Admin struct {
		Enabled          bool   `yaml:"enabled"`
		HistoryDir       string `yaml:"history-dir"`
		CloudflareAccess struct {
			TeamDomain    string   `yaml:"team-domain"`
			Audience      string   `yaml:"audience"`
			AllowedEmails []string `yaml:"allowed-emails"`
		} `yaml:"cloudflare-access"`
	} `yaml:"admin"`
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `go test ./internal/glance/ -run TestConfigParsesAdminBlock -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/config.go internal/glance/admin_test.go
git commit -m "feat(admin): add admin config schema block

Off by default. Captures enabled flag, history-dir override, and
Cloudflare Access (team-domain, audience, allowed-emails). Validation
of required fields happens when the admin server mounts."
```

---

### Task 2: Add `go-oidc` dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the import**

Run:
```bash
go get github.com/coreos/go-oidc/v3@v3.11.0
go mod tidy
```

- [ ] **Step 2: Verify the build still succeeds**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 3: Verify existing tests still pass**

Run: `go test ./...`
Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "feat(admin): add github.com/coreos/go-oidc/v3 for JWT verification

Used by the admin server to verify Cloudflare Access JWTs against
Cloudflare's rotating JWKS."
```

---

### Task 3: Cloudflare Access JWT verifier

**Files:**
- Create: `internal/glance/admin_cfaccess.go`
- Create: `internal/glance/admin_cfaccess_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/glance/admin_cfaccess_test.go`:

```go
package glance

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// testJWKSServer returns a *httptest.Server that serves a JWKS containing the
// supplied RSA public key, along with the signer used to mint tokens.
func testJWKSServer(t *testing.T, key *rsa.PrivateKey, kid string) (*httptest.Server, jose.Signer) {
	t.Helper()

	jwkSet := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"use": "sig",
			"kid": kid,
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn-cgi/access/certs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwkSet)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return srv, signer
}

func mintToken(t *testing.T, signer jose.Signer, audience, email string, expiry time.Time) string {
	t.Helper()
	claims := map[string]any{
		"aud":   audience,
		"email": email,
		"exp":   expiry.Unix(),
		"iat":   time.Now().Unix(),
	}
	tok, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

func TestCFAccessVerifier_AcceptsValidToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	srv, signer := testJWKSServer(t, key, "test-kid")
	teamDomain := strings.TrimPrefix(srv.URL, "http://")
	aud := "abc123"

	v, err := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    teamDomain,
		Audience:      aud,
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}

	tok := mintToken(t, signer, aud, "you@example.com", time.Now().Add(time.Hour))
	email, err := v.verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.EqualFold(email, "you@example.com") {
		t.Fatalf("email: got %q", email)
	}
}

func TestCFAccessVerifier_RejectsWrongAudience(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, signer := testJWKSServer(t, key, "k1")
	v, _ := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    strings.TrimPrefix(srv.URL, "http://"),
		Audience:      "expected-aud",
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})

	tok := mintToken(t, signer, "wrong-aud", "you@example.com", time.Now().Add(time.Hour))
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected error for wrong audience")
	}
}

func TestCFAccessVerifier_RejectsEmailNotInAllowlist(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, signer := testJWKSServer(t, key, "k1")
	v, _ := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    strings.TrimPrefix(srv.URL, "http://"),
		Audience:      "abc",
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})

	tok := mintToken(t, signer, "abc", "other@example.com", time.Now().Add(time.Hour))
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected error for email not in allowlist")
	}
}

func TestCFAccessVerifier_RejectsExpiredToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, signer := testJWKSServer(t, key, "k1")
	v, _ := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    strings.TrimPrefix(srv.URL, "http://"),
		Audience:      "abc",
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})

	tok := mintToken(t, signer, "abc", "you@example.com", time.Now().Add(-time.Minute))
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected error for expired token")
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestCFAccessVerifier -v`
Expected: FAIL — `newCFAccessVerifier` / `cfAccessConfig` undefined.

- [ ] **Step 3: Implement the verifier**

Create `internal/glance/admin_cfaccess.go`:

```go
package glance

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type cfAccessConfig struct {
	TeamDomain    string
	Audience      string
	AllowedEmails []string
	// InsecureHTTP allows http:// JWKS URLs. Only set in tests.
	InsecureHTTP bool
}

type cfAccessVerifier struct {
	cfg      cfAccessConfig
	verifier *oidc.IDTokenVerifier
	allowed  map[string]struct{} // lowercase emails for O(1) lookup
}

func newCFAccessVerifier(ctx context.Context, cfg cfAccessConfig) (*cfAccessVerifier, error) {
	if cfg.TeamDomain == "" {
		return nil, fmt.Errorf("cloudflare-access.team-domain is required")
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("cloudflare-access.audience is required")
	}
	if len(cfg.AllowedEmails) == 0 {
		return nil, fmt.Errorf("cloudflare-access.allowed-emails must list at least one address")
	}

	scheme := "https"
	if cfg.InsecureHTTP {
		scheme = "http"
	}
	certsURL := fmt.Sprintf("%s://%s/cdn-cgi/access/certs", scheme, cfg.TeamDomain)

	keySet := oidc.NewRemoteKeySet(ctx, certsURL)
	verifier := oidc.NewVerifier(
		fmt.Sprintf("%s://%s", scheme, cfg.TeamDomain),
		keySet,
		&oidc.Config{
			ClientID:             cfg.Audience,
			SkipClientIDCheck:    false,
			SkipIssuerCheck:      true, // CF Access tokens don't use a standard iss
			SupportedSigningAlgs: []string{"RS256"},
		},
	)

	allowed := make(map[string]struct{}, len(cfg.AllowedEmails))
	for _, e := range cfg.AllowedEmails {
		allowed[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}

	return &cfAccessVerifier{cfg: cfg, verifier: verifier, allowed: allowed}, nil
}

func (v *cfAccessVerifier) verify(ctx context.Context, rawToken string) (email string, err error) {
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", fmt.Errorf("jwt verification: %w", err)
	}

	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := tok.Claims(&claims); err != nil {
		return "", fmt.Errorf("parsing claims: %w", err)
	}
	if claims.Email == "" {
		return "", fmt.Errorf("jwt has no email claim")
	}

	if _, ok := v.allowed[strings.ToLower(claims.Email)]; !ok {
		return "", fmt.Errorf("email %q not in allowlist", claims.Email)
	}

	return claims.Email, nil
}

// verifyFromRequest is a convenience wrapper that extracts the header.
func (v *cfAccessVerifier) verifyFromRequest(r *http.Request) (email string, err error) {
	tok := r.Header.Get("Cf-Access-Jwt-Assertion")
	if tok == "" {
		return "", fmt.Errorf("missing Cf-Access-Jwt-Assertion header")
	}
	return v.verify(r.Context(), tok)
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestCFAccessVerifier -v`
Expected: all four tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin_cfaccess.go internal/glance/admin_cfaccess_test.go
git commit -m "feat(admin): Cloudflare Access JWT verifier

Uses coreos/go-oidc for JWKS fetching and signature verification.
Verifies audience claim and email allowlist. Case-insensitive email
comparison. Tests cover valid, wrong-audience, not-in-allowlist, and
expired-token paths using an in-test JWKS server."
```

---

### Task 4: Admin server skeleton — mount gating + dual auth

**Files:**
- Create: `internal/glance/admin.go`
- Modify: `internal/glance/admin_test.go` — add integration test

- [ ] **Step 1: Write the failing integration test**

Append to `internal/glance/admin_test.go`:

```go
import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func newTestAdminServer(t *testing.T) (*adminServer, *cfAccessVerifier, jose.Signer, string) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwksSrv, signer := testJWKSServer(t, key, "k1")
	teamDomain := strings.TrimPrefix(jwksSrv.URL, "http://")
	aud := "aud-xyz"
	verifier, err := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain: teamDomain, Audience: aud,
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	a := &adminServer{cfAccess: verifier, devBypass: false}
	return a, verifier, signer, aud
}

func TestAdminMountRejectsMissingCFJWT(t *testing.T) {
	a, _, _, _ := newTestAdminServer(t)
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin", nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
	if got := rw.Header().Get("X-Admin-Auth-Failed"); got != "cloudflare-access" {
		t.Fatalf("expected X-Admin-Auth-Failed=cloudflare-access, got %q", got)
	}
}

func TestAdminMountAcceptsValidCFJWTButStillRequiresSession(t *testing.T) {
	a, _, signer, aud := newTestAdminServer(t)
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", mintToken(t, signer, aud, "you@example.com", time.Now().Add(time.Hour)))
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
	if got := rw.Header().Get("X-Admin-Auth-Failed"); got != "session" {
		t.Fatalf("expected X-Admin-Auth-Failed=session, got %q", got)
	}
}

func TestAdminDevBypassSkipsCFButKeepsSession(t *testing.T) {
	a, _, _, _ := newTestAdminServer(t)
	a.devBypass = true
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin", nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
	if got := rw.Header().Get("X-Admin-Auth-Failed"); got != "session" {
		t.Fatalf("expected X-Admin-Auth-Failed=session, got %q", got)
	}
	_ = os.Setenv // keep import
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestAdmin -v`
Expected: FAIL — `adminServer`, `registerRoutes` undefined.

- [ ] **Step 3: Implement the minimal admin server**

Create `internal/glance/admin.go`:

```go
package glance

import (
	"context"
	"log"
	"net/http"
	"os"
)

// adminServer owns the /admin surface. Constructed fresh on every config
// reload; long-lived state (preview registry, git repo) is reattached below.
type adminServer struct {
	// liveApp returns the current running application so preview can clone config.
	liveApp func() *application

	cfAccess  *cfAccessVerifier
	devBypass bool

	// Populated in later tasks.
	history   *gitHistory
	previews  *previewRegistry
	filePaths []string // main config + includes, absolute paths
}

func newAdminServer(ctx context.Context, cfg *config, liveApp func() *application, configPath string, includes []string) (*adminServer, error) {
	devBypass := os.Getenv("GLANCE_ADMIN_DEV_BYPASS") == "1"

	var verifier *cfAccessVerifier
	if !devBypass {
		var err error
		verifier, err = newCFAccessVerifier(ctx, cfAccessConfig{
			TeamDomain:    cfg.Admin.CloudflareAccess.TeamDomain,
			Audience:      cfg.Admin.CloudflareAccess.Audience,
			AllowedEmails: cfg.Admin.CloudflareAccess.AllowedEmails,
		})
		if err != nil {
			return nil, err
		}
	} else {
		log.Printf("WARNING: GLANCE_ADMIN_DEV_BYPASS is set — Cloudflare Access verification is skipped")
	}

	paths := append([]string{configPath}, includes...)

	return &adminServer{
		liveApp:   liveApp,
		cfAccess:  verifier,
		devBypass: devBypass,
		filePaths: paths,
	}, nil
}

// middleware composes the CF Access check and the Glance session check.
func (a *adminServer) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Layer 1: CF Access JWT (skipped in dev bypass).
		if !a.devBypass {
			if _, err := a.cfAccess.verifyFromRequest(r); err != nil {
				w.Header().Set("X-Admin-Auth-Failed", "cloudflare-access")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}

		// Layer 2: Glance session cookie.
		if app := a.getLiveApp(); app == nil || !app.isAuthorized(w, r) {
			w.Header().Set("X-Admin-Auth-Failed", "session")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

func (a *adminServer) getLiveApp() *application {
	if a.liveApp == nil {
		return nil
	}
	return a.liveApp()
}

// registerRoutes mounts the admin handlers. prefix is the path prefix (e.g. "/admin").
func (a *adminServer) registerRoutes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix, a.middleware(a.handleIndex))
}

func (a *adminServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Placeholder — replaced with the editor shell in Task 15.
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("admin ok"))
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestAdmin -v`
Expected: the three admin mount tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): adminServer skeleton with dual-auth middleware

CF Access JWT verification (skippable via GLANCE_ADMIN_DEV_BYPASS=1)
and Glance session cookie check layered on every route. Response
header X-Admin-Auth-Failed records which layer rejected, aiding
client-side debugging without leaking info to unauthenticated callers."
```

---

### Task 5: Wire admin mounting into main.go + glance.go

**Files:**
- Modify: `internal/glance/main.go` — pass live `*application` getter to admin and mount on startup when enabled
- Modify: `internal/glance/glance.go` — hold an optional admin server pointer and call its `registerRoutes` from `server()`

- [ ] **Step 1: Write the failing test**

Append to `internal/glance/admin_test.go`:

```go
func TestAdminMountReasonsAreExplicit(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring of reason
	}{
		{
			name: "disabled",
			yaml: "admin:\n  enabled: false\n",
			want: "admin.enabled",
		},
		{
			name: "no users",
			yaml: "admin:\n  enabled: true\n",
			want: "auth.users",
		},
		{
			name: "no CF config",
			yaml: `
auth:
  secret-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
  users:
    cory:
      password: hunter2
admin:
  enabled: true
`,
			want: "cloudflare-access",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := newConfigFromYAML([]byte(tc.yaml + `
pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: clock
`))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			ok, reason := adminShouldMount(cfg)
			if ok {
				t.Fatalf("expected NOT to mount")
			}
			if !strings.Contains(reason, tc.want) {
				t.Fatalf("reason %q does not contain %q", reason, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestAdminMountReasons -v`
Expected: FAIL — `adminShouldMount` undefined.

- [ ] **Step 3: Implement `adminShouldMount` in `admin.go`**

Add to `internal/glance/admin.go`:

```go
// adminShouldMount reports whether the admin surface should be mounted and,
// if not, a human-readable reason suitable for startup logs.
func adminShouldMount(cfg *config) (bool, string) {
	if !cfg.Admin.Enabled {
		return false, "admin.enabled is false"
	}
	if len(cfg.Auth.Users) == 0 {
		return false, "auth.users must be configured before admin can mount"
	}
	devBypass := os.Getenv("GLANCE_ADMIN_DEV_BYPASS") == "1"
	if !devBypass {
		if cfg.Admin.CloudflareAccess.TeamDomain == "" ||
			cfg.Admin.CloudflareAccess.Audience == "" ||
			len(cfg.Admin.CloudflareAccess.AllowedEmails) == 0 {
			return false, "admin.cloudflare-access.{team-domain,audience,allowed-emails} all required unless GLANCE_ADMIN_DEV_BYPASS=1"
		}
	}
	return true, ""
}

func includesAsSlice(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 4: Wire mounting into `glance.go` `server()`**

In `internal/glance/glance.go`, extend the `application` struct (around line 30) with an admin field:

```go
	admin *adminServer
```

In `application.server()` (around line 436), add this block just before `server := http.Server{...}`:

```go
	if a.admin != nil {
		a.admin.registerRoutes(mux, a.Config.Server.BaseURL+"/admin")
	}
```

- [ ] **Step 5: Wire construction into `main.go`**

In `internal/glance/main.go`, inside `serveApp`'s `onChange` callback, after `app, err := newApplication(config)`, add:

```go
		if ok, reason := adminShouldMount(config); ok {
			liveAppRef := app
			admin, aerr := newAdminServer(context.Background(), config, func() *application { return liveAppRef }, configPath, includesAsSlice(configIncludes))
			if aerr != nil {
				log.Printf("admin: failed to initialize, not mounting: %v", aerr)
			} else {
				app.admin = admin
				log.Printf("admin: enabled on /admin")
			}
		} else {
			log.Printf("admin: not mounted (%s)", reason)
		}
```

Add `"context"` to main.go's imports.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/glance/ -run TestAdmin -v && go build ./...`
Expected: all admin tests pass; build succeeds.

- [ ] **Step 7: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go internal/glance/glance.go internal/glance/main.go
git commit -m "feat(admin): mount /admin when all preconditions are met

Three-factor opt-in: admin.enabled + auth.users configured + CF Access
configured (or GLANCE_ADMIN_DEV_BYPASS=1). adminShouldMount() returns
an explicit reason for the startup log so misconfigurations are easy to
spot."
```

---

### Task 6: Files API — list editable files

**Files:**
- Modify: `internal/glance/admin.go` — add `handleListFiles`
- Modify: `internal/glance/admin_test.go` — add list-files test

- [ ] **Step 1: Write the failing test**

Append to `admin_test.go` (also add `"encoding/json"` to the imports):

```go
func TestAdminListFilesReturnsMainAndIncludes(t *testing.T) {
	tmp := t.TempDir()
	main := tmp + "/glance.yml"
	inc := tmp + "/pages/home.yml"
	if err := os.MkdirAll(tmp+"/pages", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte("pages: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inc, []byte("- name: Home\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &adminServer{
		devBypass: true,
		filePaths: []string{main, inc},
		liveApp:   func() *application { return &application{} },
	}

	req := httptest.NewRequest("GET", "/admin/api/files", nil)
	rw := httptest.NewRecorder()
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status: got %d; body=%s", rw.Code, rw.Body.String())
	}

	var got []fileListEntry
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2", len(got))
	}
	if got[0].Path != main {
		t.Fatalf("expected main first, got %q", got[0].Path)
	}
	if got[0].Size == 0 {
		t.Fatalf("expected non-zero size")
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

Run: `go test ./internal/glance/ -run TestAdminListFiles -v`
Expected: FAIL.

- [ ] **Step 3: Implement the list endpoint**

In `admin.go`, update `registerRoutes` to include:

```go
	mux.HandleFunc("GET "+prefix+"/api/files", a.middleware(a.handleListFiles))
```

Add imports `"encoding/json"` and `"os"`. Add:

```go
type fileListEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"` // unix seconds
}

func (a *adminServer) handleListFiles(w http.ResponseWriter, r *http.Request) {
	out := make([]fileListEntry, 0, len(a.filePaths))
	for _, p := range a.filePaths {
		info, err := os.Stat(p)
		if err != nil {
			out = append(out, fileListEntry{Path: p})
			continue
		}
		out = append(out, fileListEntry{
			Path: p, Size: info.Size(), ModTime: info.ModTime().Unix(),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `go test ./internal/glance/ -run TestAdminListFiles -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): GET /admin/api/files returns main + includes

Each entry has absolute path, byte size, and unix mtime. Includes the
main config first so the client always knows the root."
```

---

### Task 7: Files API — read single file

**Files:**
- Modify: `internal/glance/admin.go` — add `handleReadFile`, traversal guard
- Modify: `internal/glance/admin_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `admin_test.go`:

```go
func TestAdminReadFileReturnsContents(t *testing.T) {
	tmp := t.TempDir()
	main := tmp + "/glance.yml"
	content := "pages: []\n"
	if err := os.WriteFile(main, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &adminServer{
		devBypass: true, filePaths: []string{main},
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin/api/files/"+main, nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status %d; body=%s", rw.Code, rw.Body.String())
	}
	if rw.Body.String() != content {
		t.Fatalf("body %q != %q", rw.Body.String(), content)
	}
}

func TestAdminReadFileRejectsPathOutsideIncludeSet(t *testing.T) {
	tmp := t.TempDir()
	allowed := tmp + "/glance.yml"
	forbidden := tmp + "/etc-passwd"
	_ = os.WriteFile(allowed, []byte("pages: []\n"), 0o600)
	_ = os.WriteFile(forbidden, []byte("secret\n"), 0o600)

	a := &adminServer{
		devBypass: true, filePaths: []string{allowed},
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin/api/files/"+forbidden, nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rw.Code)
	}
	if strings.Contains(rw.Body.String(), "secret") {
		t.Fatalf("body must not leak file content")
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run "TestAdminReadFile" -v`
Expected: FAIL.

- [ ] **Step 3: Implement read + traversal guard**

In `admin.go`, add to `registerRoutes`:

```go
	mux.HandleFunc("GET "+prefix+"/api/files/{path...}", a.middleware(a.handleReadFile))
```

Add the imports `"path/filepath"` and:

```go
// allowedFile returns the absolute path if it's in the include set, else "".
func (a *adminServer) allowedFile(raw string) string {
	clean, err := filepath.Abs(raw)
	if err != nil {
		return ""
	}
	for _, p := range a.filePaths {
		pAbs, _ := filepath.Abs(p)
		if pAbs == clean {
			return clean
		}
	}
	return ""
}

func (a *adminServer) handleReadFile(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("path")
	abs := a.allowedFile("/" + raw)
	if abs == "" {
		abs = a.allowedFile(raw)
	}
	if abs == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run "TestAdminReadFile" -v`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): GET /admin/api/files/{path} with traversal guard

Only paths in the include set are served, compared on resolved absolute
paths. Unknown paths return 403 with no body to avoid leaking existence
information."
```

---

### Task 8: Files API — dry-run validate

**Files:**
- Modify: `internal/glance/admin.go` — add `handleValidate`
- Modify: `internal/glance/admin_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `admin_test.go`:

```go
func TestAdminValidateAcceptsValidConfig(t *testing.T) {
	a := &adminServer{
		devBypass: true, filePaths: []string{"/dev/null"},
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	body := strings.NewReader(`pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: clock
`)
	req := httptest.NewRequest("POST", "/admin/api/validate", body)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
}

func TestAdminValidateRejectsInvalidConfig(t *testing.T) {
	a := &adminServer{
		devBypass: true, filePaths: []string{"/dev/null"},
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	body := strings.NewReader(`pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: not-a-real-widget
`)
	req := httptest.NewRequest("POST", "/admin/api/validate", body)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "error") {
		t.Fatalf("body should include error message, got %q", rw.Body.String())
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestAdminValidate -v`
Expected: FAIL.

- [ ] **Step 3: Implement the validate endpoint**

In `admin.go`, add to `registerRoutes`:

```go
	mux.HandleFunc("POST "+prefix+"/api/validate", a.middleware(a.handleValidate))
```

Add `"io"` to imports. Add:

```go
type validateResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (a *adminServer) handleValidate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(validateResponse{Error: "body read: " + err.Error()})
		return
	}
	if _, err := newConfigFromYAML(body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(validateResponse{Error: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(validateResponse{OK: true})
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestAdminValidate -v`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): POST /admin/api/validate for dry-run YAML check

Body-only validation; no disk write. 1MB body limit. Returns the
go-yaml error message directly so the client can surface it in the
editor error strip."
```

---

### Task 9: Git history wrapper

**Files:**
- Create: `internal/glance/admin_history.go`
- Create: `internal/glance/admin_history_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/glance/admin_history_test.go`:

```go
package glance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH, skipping")
	}
}

func TestGitHistoryInitsAndCommits(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	historyDir := tmp + "/.glance-history"

	realCfg := tmp + "/glance.yml"
	if err := os.WriteFile(realCfg, []byte("pages: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h, err := openGitHistory(historyDir, []string{realCfg})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.recordInitial(); err != nil {
		t.Fatalf("initial: %v", err)
	}

	newContents := []byte("pages:\n  - name: X\n")
	if err := h.commitEdit(realCfg, newContents,
		gitCommitter{Email: "you@example.com", Name: "You"},
		"edit glance.yml · 2026-01-01T00:00:00Z · you@example.com"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	mirror := h.mirrorPath(realCfg)
	got, err := os.ReadFile(mirror)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if string(got) != string(newContents) {
		t.Fatalf("mirror mismatch: got %q", got)
	}

	entries, err := h.log(10)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(entries))
	}
	if !strings.Contains(entries[0].Message, "edit") {
		t.Fatalf("newest message: got %q", entries[0].Message)
	}
	_ = filepath.Base // silence unused
}

func TestGitHistoryRollback(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	realCfg := tmp + "/glance.yml"
	_ = os.WriteFile(realCfg, []byte("pages: []\n"), 0o600)
	h, err := openGitHistory(tmp+"/.glance-history", []string{realCfg})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.recordInitial()
	_ = h.commitEdit(realCfg, []byte("pages:\n  - name: A\n"),
		gitCommitter{Email: "a@x", Name: "a"}, "edit 1")
	if err := h.rollbackLast(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	entries, _ := h.log(10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 commit after rollback, got %d", len(entries))
	}
}

func TestGitHistoryRestore(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	realCfg := tmp + "/glance.yml"
	_ = os.WriteFile(realCfg, []byte("pages: []\n"), 0o600)
	h, _ := openGitHistory(tmp+"/.glance-history", []string{realCfg})
	_ = h.recordInitial()
	_ = h.commitEdit(realCfg, []byte("pages:\n  - name: A\n"),
		gitCommitter{Email: "a@x", Name: "a"}, "edit A")
	_ = h.commitEdit(realCfg, []byte("pages:\n  - name: B\n"),
		gitCommitter{Email: "b@x", Name: "b"}, "edit B")

	entries, _ := h.log(10)
	// entries[2] is the initial commit.
	restored, err := h.restore(entries[2].SHA, gitCommitter{Email: "r@x", Name: "r"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, _ := os.ReadFile(restored[0]); string(got) != "pages: []\n" {
		t.Fatalf("restore did not write initial contents, got %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestGitHistory -v`
Expected: FAIL.

- [ ] **Step 3: Implement the git wrapper**

Create `internal/glance/admin_history.go`:

```go
package glance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type gitCommitter struct {
	Email, Name string
}

type gitHistoryEntry struct {
	SHA     string    `json:"SHA"`
	Time    time.Time `json:"Time"`
	Author  string    `json:"Author"`
	Email   string    `json:"Email"`
	Message string    `json:"Message"`
}

type gitHistory struct {
	dir        string   // absolute path to .glance-history/
	trackPaths []string // absolute real-world paths of config files
}

func openGitHistory(dir string, realFiles []string) (*gitHistory, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	absFiles := make([]string, len(realFiles))
	for i, f := range realFiles {
		absFiles[i], _ = filepath.Abs(f)
	}

	h := &gitHistory{dir: abs, trackPaths: absFiles}
	if _, err := os.Stat(filepath.Join(abs, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, err
		}
		if _, err := h.git("init"); err != nil {
			return nil, fmt.Errorf("git init: %w", err)
		}
	}
	return h, nil
}

func (h *gitHistory) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", h.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v (stderr: %s)", strings.Join(args, " "), err, errb.String())
	}
	return out.String(), nil
}

func (h *gitHistory) mirrorPath(real string) string {
	abs, _ := filepath.Abs(real)
	rel := strings.TrimPrefix(abs, string(os.PathSeparator))
	rel = strings.ReplaceAll(rel, string(os.PathSeparator), "__")
	return filepath.Join(h.dir, rel)
}

func (h *gitHistory) recordInitial() error {
	for _, p := range h.trackPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m := h.mirrorPath(p)
		if err := os.MkdirAll(filepath.Dir(m), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(m, data, 0o600); err != nil {
			return err
		}
	}
	if _, err := h.git("add", "--all"); err != nil {
		return err
	}
	if _, err := h.gitCommit(gitCommitter{Email: "admin@glance.local", Name: "Glance Admin"},
		"initial · "+time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return nil
}

func (h *gitHistory) gitCommit(c gitCommitter, message string) (string, error) {
	_, err := h.git(
		"-c", "user.email="+c.Email,
		"-c", "user.name="+c.Name,
		"commit", "-m", message,
	)
	return message, err
}

func (h *gitHistory) commitEdit(realPath string, newContents []byte, c gitCommitter, message string) error {
	m := h.mirrorPath(realPath)
	if err := os.MkdirAll(filepath.Dir(m), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(m, newContents, 0o600); err != nil {
		return err
	}
	if _, err := h.git("add", "--all"); err != nil {
		return err
	}
	_, err := h.gitCommit(c, message)
	return err
}

func (h *gitHistory) rollbackLast() error {
	_, err := h.git("reset", "--hard", "HEAD~1")
	return err
}

func (h *gitHistory) log(n int) ([]gitHistoryEntry, error) {
	fmtStr := "%H%x1f%cI%x1f%an%x1f%ae%x1f%s%x1e"
	out, err := h.git("log", fmt.Sprintf("-n%d", n), "--pretty=format:"+fmtStr)
	if err != nil {
		return nil, err
	}
	var entries []gitHistoryEntry
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		parts := strings.Split(rec, "\x1f")
		if len(parts) < 5 {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, parts[1])
		entries = append(entries, gitHistoryEntry{
			SHA: parts[0], Time: ts, Author: parts[2], Email: parts[3], Message: parts[4],
		})
	}
	return entries, nil
}

func (h *gitHistory) restore(sha string, c gitCommitter) ([]string, error) {
	for _, p := range h.trackPaths {
		m := h.mirrorPath(p)
		rel, err := filepath.Rel(h.dir, m)
		if err != nil {
			return nil, err
		}
		if _, err := h.git("checkout", sha, "--", rel); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return nil, err
		}
	}
	if _, err := h.git("add", "--all"); err != nil {
		return nil, err
	}
	shortSHA := sha
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}
	if _, err := h.gitCommit(c, fmt.Sprintf("restore %s · %s", shortSHA, time.Now().UTC().Format(time.RFC3339))); err != nil {
		return nil, err
	}
	return h.trackPaths, nil
}

func (h *gitHistory) diff(sha string) (string, error) {
	return h.git("show", "--format=", sha)
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestGitHistory -v`
Expected: all three PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin_history.go internal/glance/admin_history_test.go
git commit -m "feat(admin): git-backed history wrapper

Thin shell-out over system git. Each tracked real-world file is
mirrored into the history repo under a name derived from its absolute
path. Supports recordInitial, commitEdit, rollbackLast (for failed real
writes), log, diff, and restore (which rewrites real files and records
an append-only restore commit)."
```

---

### Task 10: Save flow — PUT /admin/api/files/{path}

**Files:**
- Modify: `internal/glance/admin.go` — add `handleWriteFile`, mutex, generation counter
- Modify: `internal/glance/admin_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `admin_test.go`:

```go
func TestAdminSaveValidYAMLWritesFileAndCommits(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	cfgPath := tmp + "/glance.yml"
	_ = os.WriteFile(cfgPath, []byte(`pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: clock
`), 0o600)

	history, err := openGitHistory(tmp+"/.glance-history", []string{cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	_ = history.recordInitial()

	a := &adminServer{
		devBypass: true, filePaths: []string{cfgPath},
		history: history,
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	newBody := `pages:
  - name: NewHome
    columns:
      - size: full
        widgets:
          - type: clock
`
	req := httptest.NewRequest("PUT", "/admin/api/files/"+cfgPath, strings.NewReader(newBody))
	req.Header.Set("Cf-Access-Email", "you@example.com")
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status %d; body=%s", rw.Code, rw.Body.String())
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != newBody {
		t.Fatalf("file contents mismatch")
	}
	entries, _ := history.log(10)
	if len(entries) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(entries))
	}
}

func TestAdminSaveInvalidYAMLReturns400AndDoesNotWrite(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	cfgPath := tmp + "/glance.yml"
	orig := `pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: clock
`
	_ = os.WriteFile(cfgPath, []byte(orig), 0o600)
	history, _ := openGitHistory(tmp+"/.glance-history", []string{cfgPath})
	_ = history.recordInitial()

	a := &adminServer{
		devBypass: true, filePaths: []string{cfgPath},
		history: history,
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	bad := `pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: not-a-real-widget
`
	req := httptest.NewRequest("PUT", "/admin/api/files/"+cfgPath, strings.NewReader(bad))
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rw.Code)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != orig {
		t.Fatalf("file should not have changed")
	}
	entries, _ := history.log(10)
	if len(entries) != 1 {
		t.Fatalf("expected only initial commit, got %d", len(entries))
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestAdminSave -v`
Expected: FAIL.

- [ ] **Step 3: Implement the save handler**

In `admin.go`, add `"sync"`, `"sync/atomic"`, `"time"` imports. Extend `adminServer`:

```go
type adminServer struct {
	liveApp func() *application
	cfAccess  *cfAccessVerifier
	devBypass bool

	history   *gitHistory
	previews  *previewRegistry
	filePaths []string

	saveMu           sync.Mutex
	configGeneration atomic.Int64

	sessionKeyFn func(r *http.Request) string // overridable in tests
}
```

Add to `registerRoutes`:

```go
	mux.HandleFunc("PUT "+prefix+"/api/files/{path...}", a.middleware(a.handleWriteFile))
	mux.HandleFunc("GET "+prefix+"/api/config-generation", a.middleware(a.handleConfigGeneration))
```

Add handlers:

```go
func (a *adminServer) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("path")
	abs := a.allowedFile("/" + raw)
	if abs == "" {
		abs = a.allowedFile(raw)
	}
	if abs == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validation scope:
	//   - Editing the main config (filePaths[0]): validate the full body.
	//   - Editing an include: body is a YAML fragment (e.g. a list of pages)
	//     and cannot be validated as a standalone config. Skip strict
	//     validation; Glance's fsnotify watcher will log and reject a bad
	//     reload while keeping the previous config live. This matches the
	//     MVP scope called out in the spec's "Known risks" section; a later
	//     iteration can add include-aware validation via a virtual file set.
	if len(a.filePaths) > 0 && abs == a.filePaths[0] {
		if _, err := newConfigFromYAML(body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(validateResponse{Error: err.Error()})
			return
		}
	}

	a.saveMu.Lock()
	defer a.saveMu.Unlock()

	committer := committerFromRequest(r)
	msg := "edit " + filepath.Base(abs) + " · " + time.Now().UTC().Format(time.RFC3339) + " · " + committer.Email
	if err := a.history.commitEdit(abs, body, committer, msg); err != nil {
		http.Error(w, "history commit failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(abs, body, 0o600); err != nil {
		_ = a.history.rollbackLast()
		http.Error(w, "disk write failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	a.configGeneration.Add(1)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":                true,
		"config_generation": a.configGeneration.Load(),
	})
}

func (a *adminServer) handleConfigGeneration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"generation": a.configGeneration.Load()})
}

func committerFromRequest(r *http.Request) gitCommitter {
	email := r.Header.Get("Cf-Access-Email")
	name := r.Header.Get("Cf-Access-Name")
	if email == "" {
		email = "admin@glance.local"
	}
	if name == "" {
		if at := strings.Index(email, "@"); at > 0 {
			name = email[:at]
		} else {
			name = "Glance Admin"
		}
	}
	return gitCommitter{Email: email, Name: name}
}
```

Add `"strings"` to imports.

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestAdminSave -v`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): PUT /admin/api/files/{path} with validate + commit

Validates the pending body via newConfigFromYAML; on success commits to
the history repo first then writes the real path. If the real write
fails we rollback the history commit (git reset --hard HEAD~1) so
history never drifts ahead of the running config. Committer email/name
come from CF-Access headers when present."
```

---

### Task 11: History API — list, diff, restore

**Files:**
- Modify: `internal/glance/admin.go` — history endpoints
- Modify: `internal/glance/admin_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `admin_test.go`:

```go
func TestAdminHistoryListReturnsEntries(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	cfgPath := tmp + "/glance.yml"
	_ = os.WriteFile(cfgPath, []byte("pages: []\n"), 0o600)
	history, _ := openGitHistory(tmp+"/.glance-history", []string{cfgPath})
	_ = history.recordInitial()
	_ = history.commitEdit(cfgPath, []byte("pages:\n  - name: A\n"),
		gitCommitter{Email: "you@example.com", Name: "you"}, "edit")

	a := &adminServer{
		devBypass: true, history: history, filePaths: []string{cfgPath},
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin/api/history", nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rw.Code, rw.Body.String())
	}
	var entries []gitHistoryEntry
	_ = json.Unmarshal(rw.Body.Bytes(), &entries)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
}

func TestAdminHistoryRestoreRewritesFile(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	cfgPath := tmp + "/glance.yml"
	orig := "pages: []\n"
	_ = os.WriteFile(cfgPath, []byte(orig), 0o600)
	history, _ := openGitHistory(tmp+"/.glance-history", []string{cfgPath})
	_ = history.recordInitial()
	_ = history.commitEdit(cfgPath, []byte("pages:\n  - name: A\n"),
		gitCommitter{Email: "you@example.com", Name: "you"}, "edit")

	a := &adminServer{
		devBypass: true, history: history, filePaths: []string{cfgPath},
		liveApp: func() *application { return &application{} },
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	entries, _ := history.log(10)
	initialSHA := entries[1].SHA

	req := httptest.NewRequest("POST", "/admin/api/history/"+initialSHA+"/restore", nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rw.Code, rw.Body.String())
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != orig {
		t.Fatalf("file did not revert; got %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestAdminHistory -v`
Expected: FAIL.

- [ ] **Step 3: Implement the history endpoints**

In `admin.go`, add to `registerRoutes`:

```go
	mux.HandleFunc("GET "+prefix+"/api/history", a.middleware(a.handleHistoryList))
	mux.HandleFunc("GET "+prefix+"/api/history/{sha}/diff", a.middleware(a.handleHistoryDiff))
	mux.HandleFunc("POST "+prefix+"/api/history/{sha}/restore", a.middleware(a.handleHistoryRestore))
```

Add:

```go
func (a *adminServer) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	entries, err := a.history.log(50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

func (a *adminServer) handleHistoryDiff(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	if !looksLikeSHA(sha) {
		http.Error(w, "bad sha", http.StatusBadRequest)
		return
	}
	diff, err := a.history.diff(sha)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(diff))
}

func (a *adminServer) handleHistoryRestore(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	if !looksLikeSHA(sha) {
		http.Error(w, "bad sha", http.StatusBadRequest)
		return
	}
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	if _, err := a.history.restore(sha, committerFromRequest(r)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.configGeneration.Add(1)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func looksLikeSHA(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestAdminHistory -v`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): history list, diff, and restore endpoints

GET /admin/api/history returns up to 50 latest commits with time,
author, email, message. GET .../diff returns unified diff. POST
.../restore rewrites real config files and records an append-only
restore commit. SHA format validated before shell-out."
```

---

### Task 12: Preview registry

**Files:**
- Create: `internal/glance/admin_preview.go`
- Create: `internal/glance/admin_preview_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/glance/admin_preview_test.go`:

```go
package glance

import (
	"testing"
	"time"
)

func TestPreviewRegistryStoresAndLooksUp(t *testing.T) {
	reg := newPreviewRegistry(3, 5*time.Minute)
	app := &application{}
	id := reg.put("session-A", app)
	if id == "" {
		t.Fatal("empty id")
	}
	got := reg.get(id, "session-A")
	if got != app {
		t.Fatalf("mismatch: got %v", got)
	}
}

func TestPreviewRegistryRejectsCrossSessionLookup(t *testing.T) {
	reg := newPreviewRegistry(3, 5*time.Minute)
	id := reg.put("session-A", &application{})
	if got := reg.get(id, "session-B"); got != nil {
		t.Fatal("cross-session lookup should return nil")
	}
}

func TestPreviewRegistryLRUEvicts(t *testing.T) {
	reg := newPreviewRegistry(2, 5*time.Minute)
	id1 := reg.put("s", &application{})
	id2 := reg.put("s", &application{})
	id3 := reg.put("s", &application{})
	if got := reg.get(id1, "s"); got != nil {
		t.Fatal("id1 should have been evicted (capacity 2)")
	}
	if got := reg.get(id2, "s"); got == nil {
		t.Fatal("id2 should still be present")
	}
	if got := reg.get(id3, "s"); got == nil {
		t.Fatal("id3 should still be present")
	}
}

func TestPreviewRegistryTTLEvicts(t *testing.T) {
	reg := newPreviewRegistry(3, 10*time.Millisecond)
	id := reg.put("s", &application{})
	time.Sleep(30 * time.Millisecond)
	reg.evictExpired()
	if got := reg.get(id, "s"); got != nil {
		t.Fatal("entry should have expired")
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestPreviewRegistry -v`
Expected: FAIL.

- [ ] **Step 3: Implement the registry**

Create `internal/glance/admin_preview.go`:

```go
package glance

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type previewEntry struct {
	id         string
	sessionKey string
	app        *application
	lastAccess time.Time
	elem       *list.Element
}

type previewRegistry struct {
	mu        sync.Mutex
	capacity  int
	ttl       time.Duration
	byID      map[string]*previewEntry
	bySession map[string]map[string]struct{}
	lru       *list.List
}

func newPreviewRegistry(capacity int, ttl time.Duration) *previewRegistry {
	return &previewRegistry{
		capacity: capacity, ttl: ttl,
		byID:      make(map[string]*previewEntry),
		bySession: make(map[string]map[string]struct{}),
		lru:       list.New(),
	}
}

func (r *previewRegistry) put(sessionKey string, app *application) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if sess := r.bySession[sessionKey]; len(sess) >= r.capacity {
		for e := r.lru.Back(); e != nil; e = e.Prev() {
			entry := e.Value.(*previewEntry)
			if entry.sessionKey == sessionKey {
				r.removeLocked(entry)
				break
			}
		}
	}

	var buf [16]byte
	_, _ = rand.Read(buf[:])
	id := hex.EncodeToString(buf[:])

	entry := &previewEntry{id: id, sessionKey: sessionKey, app: app, lastAccess: time.Now()}
	entry.elem = r.lru.PushFront(entry)
	r.byID[id] = entry
	if r.bySession[sessionKey] == nil {
		r.bySession[sessionKey] = make(map[string]struct{})
	}
	r.bySession[sessionKey][id] = struct{}{}
	return id
}

func (r *previewRegistry) get(id, sessionKey string) *application {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.byID[id]
	if !ok {
		return nil
	}
	if entry.sessionKey != sessionKey {
		return nil
	}
	if time.Since(entry.lastAccess) > r.ttl {
		r.removeLocked(entry)
		return nil
	}
	entry.lastAccess = time.Now()
	r.lru.MoveToFront(entry.elem)
	return entry.app
}

func (r *previewRegistry) removeLocked(entry *previewEntry) {
	delete(r.byID, entry.id)
	if set := r.bySession[entry.sessionKey]; set != nil {
		delete(set, entry.id)
		if len(set) == 0 {
			delete(r.bySession, entry.sessionKey)
		}
	}
	r.lru.Remove(entry.elem)
}

func (r *previewRegistry) evictExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for e := r.lru.Back(); e != nil; {
		entry := e.Value.(*previewEntry)
		prev := e.Prev()
		if now.Sub(entry.lastAccess) > r.ttl {
			r.removeLocked(entry)
		}
		e = prev
	}
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestPreviewRegistry -v`
Expected: all four PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin_preview.go internal/glance/admin_preview_test.go
git commit -m "feat(admin): preview app registry with TTL and per-session LRU

Session-scoped so one admin's previews are never visible to another.
Per-session capacity cap (default 3) with LRU eviction. TTL (default
5m) refreshed on access. evictExpired() is called by a ticker from
adminServer."
```

---

### Task 13: Preview HTTP endpoints

**Files:**
- Modify: `internal/glance/admin.go` — `handleCreatePreview`, `handlePreviewServe`, `buildPreviewMux`
- Modify: `internal/glance/admin_test.go`

- [ ] **Step 1: Write the failing test**

Append to `admin_test.go`:

```go
func TestAdminPreviewCreateAndServe(t *testing.T) {
	yamlSrc := `pages:
  - name: Home
    columns:
      - size: full
        widgets:
          - type: clock
`
	cfg, err := newConfigFromYAML([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = newApplication(cfg)
	if err != nil {
		t.Fatalf("newApplication: %v", err)
	}

	a := &adminServer{
		devBypass: true,
		filePaths: []string{"/dev/null"},
		previews:  newPreviewRegistry(3, 5*time.Minute),
		liveApp:   func() *application { return &application{} },
	}
	a.sessionKeyFn = func(r *http.Request) string { return "test-session" }

	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("POST", "/admin/api/preview", strings.NewReader(yamlSrc))
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("POST preview status: %d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		PreviewID string `json:"preview_id"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.PreviewID == "" {
		t.Fatalf("no preview_id")
	}

	req2 := httptest.NewRequest("GET", "/admin/preview/"+resp.PreviewID+"/", nil)
	rw2 := httptest.NewRecorder()
	mux.ServeHTTP(rw2, req2)
	if rw2.Code != http.StatusOK {
		t.Fatalf("GET preview status: %d body=%s", rw2.Code, rw2.Body.String())
	}
	if !strings.Contains(rw2.Body.String(), "<html") && !strings.Contains(rw2.Body.String(), "<!DOCTYPE") {
		t.Fatalf("preview body does not look like an HTML page")
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

Run: `go test ./internal/glance/ -run TestAdminPreview -v`
Expected: FAIL.

- [ ] **Step 3: Implement the endpoints**

In `admin.go`, add to `registerRoutes`:

```go
	mux.HandleFunc("POST "+prefix+"/api/preview", a.middleware(a.handleCreatePreview))
	mux.HandleFunc(prefix+"/preview/{id}/", a.middleware(a.handlePreviewServe))
	mux.HandleFunc(prefix+"/preview/{id}/{path...}", a.middleware(a.handlePreviewServe))
```

Add:

```go
func (a *adminServer) sessionKey(r *http.Request) string {
	if a.sessionKeyFn != nil {
		return a.sessionKeyFn(r)
	}
	c, err := r.Cookie(AUTH_SESSION_COOKIE_NAME)
	if err != nil || c.Value == "" {
		return ""
	}
	return c.Value
}

func (a *adminServer) handleCreatePreview(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := newConfigFromYAML(body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(validateResponse{Error: err.Error()})
		return
	}
	previewApp, err := newApplication(cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := a.previews.put(a.sessionKey(r), previewApp)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"preview_id": id})
}

func (a *adminServer) handlePreviewServe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	app := a.previews.get(id, a.sessionKey(r))
	if app == nil {
		http.Error(w, "preview not found", http.StatusNotFound)
		return
	}
	subPath := r.PathValue("path")
	if subPath == "" {
		subPath = "/"
	} else {
		subPath = "/" + subPath
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = subPath
	r2.RequestURI = subPath

	buildPreviewMux(app).ServeHTTP(w, r2)
}

// buildPreviewMux returns a minimal mux for serving a preview app. Mirrors the
// subset of application.server() needed to render a page; avoids auth/admin
// routes — the preview already inherited auth from its parent request.
func buildPreviewMux(app *application) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", app.handlePageRequest)
	mux.HandleFunc("GET /{page}", app.handlePageRequest)
	mux.HandleFunc("GET /api/pages/{page}/content/{$}", app.handlePageContentRequest)
	mux.HandleFunc("/api/widgets/{widget}/{path...}", app.handleWidgetRequest)
	return mux
}
```

In `newAdminServer`, initialize the preview registry and start the eviction ticker. Replace the final return with:

```go
	a := &adminServer{
		liveApp:   liveApp,
		cfAccess:  verifier,
		devBypass: devBypass,
		filePaths: paths,
		previews:  newPreviewRegistry(3, 5*time.Minute),
	}
	go func() {
		t := time.NewTicker(1 * time.Minute)
		defer t.Stop()
		for range t.C {
			a.previews.evictExpired()
		}
	}()
	return a, nil
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `go test ./internal/glance/ -run TestAdminPreview -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): POST /admin/api/preview + GET /admin/preview/{id}/*

Create a sandboxed preview application from a pending config body,
store it in the session-scoped registry, and serve it under a URL
prefix that strips /admin/preview/{id}/ before dispatching to a
preview-only mux (page routes + widget API, no auth/manifest/admin)."
```

---

### Task 14: Widget stubs — registry + endpoint

**Files:**
- Create: `internal/glance/admin_widget_stubs.go`
- Modify: `internal/glance/admin.go` — add endpoint
- Modify: `internal/glance/admin_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `admin_test.go`:

```go
func TestAdminWidgetStubsIncludesAllKnownTypes(t *testing.T) {
	for _, wt := range knownWidgetTypes() {
		stub, ok := widgetStubs[wt]
		if !ok {
			t.Errorf("no stub for widget type %q", wt)
			continue
		}
		if stub == "" {
			t.Errorf("empty stub for widget type %q", wt)
		}
		yamlSrc := "pages:\n  - name: T\n    columns:\n      - size: full\n        widgets:\n" + indentYAML(stub, 10)
		if _, err := newConfigFromYAML([]byte(yamlSrc)); err != nil {
			t.Errorf("stub for %q is not valid inside a page: %v\n--- stub ---\n%s", wt, err, stub)
		}
	}
}

func TestAdminWidgetStubsEndpointReturnsJSON(t *testing.T) {
	a := &adminServer{devBypass: true, liveApp: func() *application { return &application{} }}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin/api/widget-stubs", nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("status %d", rw.Code)
	}
	var m map[string]string
	_ = json.Unmarshal(rw.Body.Bytes(), &m)
	if m["clock"] == "" {
		t.Fatalf("no stub for clock in response")
	}
}

func indentYAML(s string, n int) string {
	pad := strings.Repeat(" ", n)
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(pad)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `go test ./internal/glance/ -run TestAdminWidgetStubs -v`
Expected: FAIL.

- [ ] **Step 3: Implement the stubs**

Create `internal/glance/admin_widget_stubs.go`:

```go
package glance

// knownWidgetTypes returns every widget type that newWidget() accepts. Keep
// in lockstep with the switch in widget.go; the stub test catches drift.
func knownWidgetTypes() []string {
	return []string{
		"calendar", "calendar-legacy", "clock", "weather", "bookmarks",
		"iframe", "html", "hacker-news", "releases", "videos",
		"markets", "reddit", "rss", "monitor", "twitch-top-games",
		"twitch-channels", "lobsters", "change-detection", "repository",
		"search", "extension", "group", "dns-stats", "split-column",
		"custom-api", "docker-containers", "server-stats", "to-do",
	}
}

// widgetStubs maps widget type to a ready-to-paste YAML block beginning with
// "- type:" so callers append directly after a "widgets:" key.
var widgetStubs = map[string]string{
	"calendar":          "- type: calendar\n  first-day-of-week: monday\n",
	"calendar-legacy":   "- type: calendar-legacy\n",
	"clock":             "- type: clock\n  hour-format: 24h\n",
	"weather":           "- type: weather\n  location: London, United Kingdom\n  units: metric\n",
	"bookmarks":         "- type: bookmarks\n  groups:\n    - links:\n        - title: Example\n          url: https://example.com\n",
	"iframe":            "- type: iframe\n  source: https://example.com\n",
	"html":              "- type: html\n  source: |\n    <p>Hello</p>\n",
	"hacker-news":       "- type: hacker-news\n  limit: 10\n",
	"releases":          "- type: releases\n  repositories:\n    - glanceapp/glance\n",
	"videos":            "- type: videos\n  channels:\n    - UCXuqSBlHAE6Xw-yeJA0Tunw\n",
	"markets":           "- type: markets\n  markets:\n    - symbol: SPY\n      name: S&P 500\n",
	"reddit":            "- type: reddit\n  subreddit: selfhosted\n",
	"rss":               "- type: rss\n  feeds:\n    - url: https://example.com/feed.xml\n",
	"monitor":           "- type: monitor\n  sites:\n    - title: Example\n      url: https://example.com\n",
	"twitch-top-games":  "- type: twitch-top-games\n  limit: 10\n",
	"twitch-channels":   "- type: twitch-channels\n  channels:\n    - theprimeagen\n",
	"lobsters":          "- type: lobsters\n  limit: 10\n",
	"change-detection":  "- type: change-detection\n  instance-url: http://localhost:5000\n  token: REDACTED\n",
	"repository":        "- type: repository\n  repository: glanceapp/glance\n",
	"search":            "- type: search\n  search-engine: duckduckgo\n",
	"extension":         "- type: extension\n  url: https://example.com/my-extension\n",
	"group":             "- type: group\n  widgets:\n    - type: hacker-news\n    - type: lobsters\n",
	"dns-stats":         "- type: dns-stats\n  service: pihole\n  url: http://pihole.local\n  password: REDACTED\n",
	"split-column":      "- type: split-column\n  widgets:\n    - type: hacker-news\n    - type: lobsters\n",
	"custom-api":        "- type: custom-api\n  url: https://api.example.com/data\n  template: |\n    <div>{{.JSON.String \"title\"}}</div>\n",
	"docker-containers": "- type: docker-containers\n  hide-by-default: false\n",
	"server-stats":      "- type: server-stats\n",
	"to-do":             "- type: to-do\n",
}
```

In `admin.go`, add to `registerRoutes`:

```go
	mux.HandleFunc("GET "+prefix+"/api/widget-stubs", a.middleware(a.handleWidgetStubs))
```

```go
func (a *adminServer) handleWidgetStubs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(widgetStubs)
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `go test ./internal/glance/ -run TestAdminWidgetStubs -v`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/glance/admin_widget_stubs.go internal/glance/admin.go internal/glance/admin_test.go
git commit -m "feat(admin): compile-time widget stubs for the insert menu

Each stub is a valid YAML widget block; a test asserts they all parse
inside a page and that every type accepted by newWidget() has an entry.
Drift between the stub list and the widget switch will fail CI."
```

---

### Task 15: Admin HTML template + CSS

**Files:**
- Create: `internal/glance/templates/admin.html` — **full DOM structure lives here, not in JS**
- Create: `internal/glance/static/css/admin.css`
- Modify: `internal/glance/admin.go` — render the template in `handleIndex`
- Modify: `internal/glance/admin_test.go` — assert the shell renders

Important: the template contains the full page layout. JS in later tasks only queries elements and wires events; it never injects HTML with interpolated content.

- [ ] **Step 1: Write the failing test**

Append to `admin_test.go`:

```go
func TestAdminIndexServesShell(t *testing.T) {
	a := &adminServer{devBypass: true, liveApp: func() *application { return &application{} }}
	mux := http.NewServeMux()
	a.registerRoutes(mux, "/admin")

	req := httptest.NewRequest("GET", "/admin", nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status %d", rw.Code)
	}
	body := rw.Body.String()
	// Structural elements that JS relies on must be in the template.
	for _, want := range []string{
		"<title>Glance Admin</title>",
		`id="admin-root"`,
		`id="editor"`,
		`id="file-list"`,
		`id="file-picker"`,
		`id="save-btn"`,
		`id="insert-widget-btn"`,
		`id="status"`,
		`id="editor-error"`,
		`id="preview-skeleton"`,
		`id="preview-iframe"`,
		`id="history-list"`,
		"admin/app.js",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

Run: `go test ./internal/glance/ -run TestAdminIndexServesShell -v`
Expected: FAIL.

- [ ] **Step 3: Create the template**

Create `internal/glance/templates/admin.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Glance Admin</title>
<link rel="stylesheet" href="{{ .App.StaticAssetPath "css/bundle.css" }}">
<link rel="stylesheet" href="{{ .App.StaticAssetPath "css/admin.css" }}">
</head>
<body class="admin-body">
<div id="admin-root" data-base-url="{{ .App.Config.Server.BaseURL }}">
  <div class="admin-topbar">
    <strong>Glance Admin</strong>
    <select id="file-picker"></select>
    <button id="save-btn" disabled>Save</button>
    <button id="insert-widget-btn">Insert widget…</button>
    <span id="status"></span>
  </div>
  <div class="admin-mobile-tabs">
    <button data-tab="edit" class="active">Edit</button>
    <button data-tab="preview">Preview</button>
    <button data-tab="history">History</button>
  </div>
  <div class="admin-main">
    <div class="admin-filelist" id="file-list"></div>
    <div class="admin-editor-wrap admin-mobile-panel active" data-tab="edit">
      <div class="admin-editor-error" id="editor-error"></div>
      <textarea class="admin-editor" id="editor" spellcheck="false"></textarea>
    </div>
    <div class="admin-preview-wrap admin-mobile-panel" data-tab="preview">
      <div class="admin-preview-tabs">
        <button data-pvtab="skeleton" class="active">Skeleton</button>
        <button data-pvtab="full">Full render</button>
      </div>
      <div class="admin-preview-body">
        <div class="admin-preview-skeleton" id="preview-skeleton"></div>
        <iframe class="admin-preview-iframe" id="preview-iframe" title="Full dashboard preview"></iframe>
      </div>
    </div>
    <div class="admin-history-list admin-mobile-panel" data-tab="history" id="history-list"></div>
  </div>
</div>
<script src="{{ .App.StaticAssetPath "js/admin/js-yaml.min.js" }}"></script>
<script src="{{ .App.StaticAssetPath "js/admin/app.js" }}"></script>
</body>
</html>
```

- [ ] **Step 4: Create the CSS**

Create `internal/glance/static/css/admin.css`:

```css
.admin-body { margin: 0; font-family: ui-sans-serif, system-ui, sans-serif; background: var(--color-bg, #0b0b0c); color: var(--color-text, #eee); min-height: 100vh; }
#admin-root { display: flex; flex-direction: column; min-height: 100vh; }
.admin-topbar { display: flex; align-items: center; gap: 0.75rem; padding: 0.5rem 1rem; border-bottom: 1px solid #2a2a2e; background: #141418; }
.admin-topbar button, .admin-topbar select { background: #1f1f25; color: inherit; border: 1px solid #2a2a2e; padding: 0.25rem 0.75rem; border-radius: 4px; cursor: pointer; font: inherit; }
.admin-topbar button:disabled { opacity: 0.5; cursor: not-allowed; }
.admin-main { display: grid; grid-template-columns: 180px 1fr 1fr; flex: 1; min-height: 0; }
.admin-filelist { border-right: 1px solid #2a2a2e; padding: 0.5rem; overflow-y: auto; }
.admin-filelist .item { padding: 0.25rem 0.5rem; cursor: pointer; border-radius: 3px; }
.admin-filelist .item.active { background: #1f1f25; }
.admin-filelist .item.dirty::after { content: " •"; color: #ffbf3f; }
.admin-editor-wrap { display: flex; flex-direction: column; min-height: 0; border-right: 1px solid #2a2a2e; }
.admin-editor-error { background: #3a1010; color: #ffb3b3; padding: 0.5rem 1rem; font-size: 0.85em; display: none; white-space: pre-wrap; }
.admin-editor-error.visible { display: block; }
.admin-editor { flex: 1; width: 100%; font-family: ui-monospace, Menlo, monospace; font-size: 13px; background: #0b0b0c; color: #ddd; border: none; resize: none; padding: 0.75rem; tab-size: 2; outline: none; }
.admin-preview-wrap { display: flex; flex-direction: column; min-height: 0; }
.admin-preview-tabs { display: flex; border-bottom: 1px solid #2a2a2e; }
.admin-preview-tabs button { flex: 1; padding: 0.5rem; background: transparent; color: inherit; border: 0; border-bottom: 2px solid transparent; cursor: pointer; font: inherit; }
.admin-preview-tabs button.active { border-bottom-color: var(--color-primary, #4a9eff); }
.admin-preview-body { flex: 1; overflow: auto; position: relative; }
.admin-preview-skeleton { padding: 1rem; }
.admin-preview-skeleton .column { display: flex; flex-direction: column; gap: 0.5rem; margin-bottom: 0.75rem; }
.admin-preview-skeleton .box { padding: 0.75rem; border: 1px dashed #333; border-radius: 4px; color: #888; }
.admin-preview-iframe { width: 100%; height: 100%; border: 0; display: none; }
.admin-preview-iframe.visible { display: block; }
.admin-history-list { padding: 0.5rem; }
.admin-history-entry { padding: 0.5rem; border-bottom: 1px solid #1f1f25; cursor: pointer; font-size: 0.85em; }
.admin-history-entry:hover { background: #1f1f25; }
.admin-history-entry .meta { color: #888; font-size: 0.85em; }

@media (max-width: 800px) {
  .admin-main { display: flex; flex-direction: column; }
  .admin-filelist { display: none; }
  .admin-mobile-tabs { display: flex; border-bottom: 1px solid #2a2a2e; }
  .admin-mobile-tabs button { flex: 1; padding: 0.5rem; background: transparent; color: inherit; border: 0; border-bottom: 2px solid transparent; cursor: pointer; font: inherit; }
  .admin-mobile-tabs button.active { border-bottom-color: var(--color-primary, #4a9eff); }
  .admin-mobile-panel { display: none; flex: 1; min-height: 0; flex-direction: column; }
  .admin-mobile-panel.active { display: flex; }
}
@media (min-width: 801px) {
  .admin-mobile-tabs { display: none; }
  .admin-mobile-panel { display: flex !important; }
}
```

- [ ] **Step 5: Wire the template to `handleIndex`**

In `admin.go`, add `"bytes"` import. Replace `handleIndex`:

```go
var adminPageTemplate = mustParseTemplate("admin.html")

func (a *adminServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	app := a.getLiveApp()
	if app == nil {
		http.Error(w, "admin not ready", http.StatusServiceUnavailable)
		return
	}
	data := &templateData{App: app}
	app.populateTemplateRequestData(&data.Request, r)
	var buf bytes.Buffer
	if err := adminPageTemplate.Execute(&buf, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
```

- [ ] **Step 6: Run the test to confirm it passes**

Run: `go test ./internal/glance/ -run TestAdminIndexServesShell -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/glance/admin.go internal/glance/templates/admin.html internal/glance/static/css/admin.css internal/glance/admin_test.go
git commit -m "feat(admin): editor shell — template + CSS

The complete DOM structure lives in admin.html. JS (landing in the
next tasks) only queries pre-existing elements and sets event
listeners; it never injects HTML with dynamic content."
```

---

### Task 16: Vendor js-yaml + minimal app.js scaffolding

**Files:**
- Create: `internal/glance/static/js/admin/js-yaml.min.js` (downloaded)
- Create: `internal/glance/static/js/admin/app.js` — scaffolding + `renderSkeleton` using DOM APIs

- [ ] **Step 1: Vendor js-yaml**

Run:

```bash
curl -sL -o /Users/cory/glance/internal/glance/static/js/admin/js-yaml.min.js \
  https://cdn.jsdelivr.net/npm/js-yaml@4.1.0/dist/js-yaml.min.js
wc -c /Users/cory/glance/internal/glance/static/js/admin/js-yaml.min.js
```

Expected: around 40,000 bytes.

- [ ] **Step 2: Create app.js with DOM-only helpers**

Create `internal/glance/static/js/admin/app.js`:

```javascript
(function () {
  'use strict';
  const root = document.getElementById('admin-root');
  const baseURL = root.dataset.baseUrl || '';

  const state = {
    files: [],
    currentPath: null,
    pendingContents: '',
    onDiskContents: '',
    dirty: false,
  };

  function api(path, opts) {
    return fetch(baseURL + '/admin/api' + path, Object.assign({ credentials: 'same-origin' }, opts || {}));
  }

  function clearChildren(el) { while (el.firstChild) el.removeChild(el.firstChild); }

  function el(tag, attrs, text) {
    const node = document.createElement(tag);
    if (attrs) {
      for (const k in attrs) {
        if (k === 'class') node.className = attrs[k];
        else if (k === 'dataset') { for (const d in attrs.dataset) node.dataset[d] = attrs.dataset[d]; }
        else node.setAttribute(k, attrs[k]);
      }
    }
    if (text != null) node.textContent = String(text);
    return node;
  }

  // Skeleton renderer — uses DOM APIs only, no innerHTML.
  function renderSkeleton(yamlText) {
    const host = document.getElementById('preview-skeleton');
    clearChildren(host);
    let parsed;
    try {
      parsed = jsyaml.load(yamlText);
    } catch (e) {
      host.appendChild(el('div', { class: 'box' }, 'YAML parse error: ' + e.message));
      return;
    }
    if (!parsed || !Array.isArray(parsed.pages)) {
      host.appendChild(el('div', { class: 'box' }, '(no pages)'));
      return;
    }
    parsed.pages.forEach(function (page) {
      const pg = el('div');
      pg.appendChild(el('h3', null, page.name || '(untitled)'));
      (page.columns || []).forEach(function (col) {
        const c = el('div', { class: 'column' });
        (col.widgets || []).forEach(function (w) {
          const title = w.title || w.type || '(widget)';
          c.appendChild(el('div', { class: 'box' }, '[' + (col.size || 'full') + '] ' + title));
        });
        pg.appendChild(c);
      });
      host.appendChild(pg);
    });
  }

  // Populate file list.
  api('/files').then(r => r.json()).then(files => {
    state.files = files;
    const picker = document.getElementById('file-picker');
    const list = document.getElementById('file-list');
    files.forEach(f => {
      const opt = el('option', { value: f.path }, f.path.split('/').pop());
      picker.appendChild(opt);

      const item = el('div', { class: 'item', dataset: { path: f.path } }, f.path.split('/').pop());
      list.appendChild(item);
    });
  }).catch(e => {
    const status = document.getElementById('status');
    status.textContent = 'failed to load files';
    console.error(e);
  });

  // Mobile tab switching.
  root.querySelectorAll('.admin-mobile-tabs button').forEach(function (btn) {
    btn.addEventListener('click', function () {
      root.querySelectorAll('.admin-mobile-tabs button').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      root.querySelectorAll('.admin-mobile-panel').forEach(p => p.classList.remove('active'));
      const target = root.querySelector('.admin-mobile-panel[data-tab="' + btn.dataset.tab + '"]');
      if (target) target.classList.add('active');
    });
  });

  // Expose for later tasks.
  window.__admin = { api, state, renderSkeleton, el, clearChildren, baseURL, root };
})();
```

- [ ] **Step 3: Smoke-test manually**

Run the server with a valid admin config (see Verification) and open `/admin`. Confirm:
1. The file picker populates with `glance.yml`.
2. Mobile tabs toggle when clicked.
3. `jsyaml` is available globally — open devtools console, type `jsyaml.load("a: 1")`, expect `{a: 1}`.

- [ ] **Step 4: Commit**

```bash
git add internal/glance/static/js/admin/js-yaml.min.js internal/glance/static/js/admin/app.js
git commit -m "feat(admin): vendor js-yaml + scaffolding JS with DOM-only helpers

app.js uses document.createElement + textContent exclusively; no
innerHTML with dynamic content. Exposes __admin.{api,state,el,...} for
later tasks to extend."
```

---

### Task 17: Editor behaviors + save roundtrip

**Files:**
- Modify: `internal/glance/static/js/admin/app.js`

- [ ] **Step 1: Extend app.js with editor + save wiring**

Append inside the existing IIFE (before the closing `})();`):

```javascript
  const { api, state, renderSkeleton, el, clearChildren } = window.__admin;
  const editor = document.getElementById('editor');
  const editorError = document.getElementById('editor-error');
  const saveBtn = document.getElementById('save-btn');
  const statusEl = document.getElementById('status');
  const filePicker = document.getElementById('file-picker');

  function setDirty(d) {
    state.dirty = d;
    saveBtn.disabled = !d;
  }
  function setError(msg) {
    editorError.textContent = msg || '';
    if (msg) editorError.classList.add('visible');
    else editorError.classList.remove('visible');
  }
  function setStatus(msg) { statusEl.textContent = msg || ''; }

  async function loadFile(path) {
    const r = await api('/files/' + encodeURI(path));
    if (!r.ok) { setStatus('load failed'); return; }
    const text = await r.text();
    state.currentPath = path;
    state.onDiskContents = text;
    state.pendingContents = text;
    editor.value = text;
    setDirty(false);
    setError('');
    renderSkeleton(text);
  }

  filePicker.addEventListener('change', () => loadFile(filePicker.value));

  editor.addEventListener('input', () => {
    state.pendingContents = editor.value;
    setDirty(editor.value !== state.onDiskContents);
    renderSkeleton(editor.value);
    // Only run strict server-side validation for the main config. Include
    // files are YAML fragments and the standalone validator will always
    // reject them; errors in the compound graph surface at reload time
    // through Glance's existing fsnotify guard.
    if (state.files.length > 0 && state.currentPath === state.files[0].path) {
      clearTimeout(editor.__validateTimer);
      editor.__validateTimer = setTimeout(validatePending, 350);
    } else {
      setError('');
    }
  });

  editor.addEventListener('keydown', (e) => {
    if (e.key === 'Tab') {
      e.preventDefault();
      const start = editor.selectionStart;
      const end = editor.selectionEnd;
      const before = editor.value.substring(0, start);
      const sel = editor.value.substring(start, end);
      const after = editor.value.substring(end);
      if (e.shiftKey) {
        const lineStart = before.lastIndexOf('\n') + 1;
        const block = editor.value.substring(lineStart, end);
        const dedented = block.replace(/^ {1,2}/gm, '');
        editor.value = editor.value.substring(0, lineStart) + dedented + after;
        editor.selectionStart = editor.selectionEnd = start;
      } else if (sel.indexOf('\n') >= 0) {
        const lineStart = before.lastIndexOf('\n') + 1;
        const block = editor.value.substring(lineStart, end);
        const indented = block.replace(/^/gm, '  ');
        editor.value = editor.value.substring(0, lineStart) + indented + after;
        editor.selectionStart = lineStart;
        editor.selectionEnd = lineStart + indented.length;
      } else {
        editor.value = before + '  ' + after;
        editor.selectionStart = editor.selectionEnd = start + 2;
      }
      editor.dispatchEvent(new Event('input'));
    } else if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      save();
    }
  });

  async function validatePending() {
    const r = await api('/validate', { method: 'POST', body: state.pendingContents, headers: { 'Content-Type': 'text/plain' } });
    if (r.ok) { setError(''); return true; }
    const body = await r.json().catch(() => ({}));
    setError(body.error || 'invalid config');
    return false;
  }

  async function currentGen() {
    const r = await api('/config-generation');
    if (!r.ok) return -1;
    const b = await r.json();
    return b.generation;
  }
  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  async function save() {
    if (!state.currentPath || !state.dirty) return;
    setStatus('saving…');
    const prevGen = await currentGen();
    const r = await api('/files/' + encodeURI(state.currentPath), {
      method: 'PUT',
      body: state.pendingContents,
      headers: { 'Content-Type': 'text/plain' },
    });
    if (!r.ok) {
      const body = await r.json().catch(() => ({}));
      setError(body.error || 'save failed');
      setStatus('save failed');
      return;
    }
    state.onDiskContents = state.pendingContents;
    setDirty(false);
    setStatus('saved; waiting for reload…');
    const deadline = Date.now() + 3000;
    while (Date.now() < deadline) {
      const g = await currentGen();
      if (g !== prevGen) { setStatus('reloaded ✓'); return; }
      await sleep(150);
    }
    setStatus('saved (no reload detected)');
  }
  saveBtn.addEventListener('click', save);

  // Select first file on load.
  const initialLoad = setInterval(() => {
    if (state.files.length > 0) {
      loadFile(state.files[0].path);
      clearInterval(initialLoad);
    }
  }, 50);

  // Expose for later tasks.
  window.__admin.loadFile = loadFile;
  window.__admin.save = save;
```

- [ ] **Step 2: Manually verify end-to-end**

Run the server (see Verification) and:
1. Open `/admin`; confirm file contents appear.
2. Type a change; skeleton preview updates; Save enables.
3. Introduce a typo; red error strip appears.
4. Fix and save; confirm `reloaded ✓` within a second.
5. Reload `/admin`; changes persist.

- [ ] **Step 3: Commit**

```bash
git add internal/glance/static/js/admin/app.js
git commit -m "feat(admin): editor behaviors + save roundtrip

Tab/shift-tab indent, Ctrl/Cmd-S save, debounced validation against
POST /admin/api/validate, and post-save polling of config-generation
so the UI reflects fsnotify's live reload. Error strip uses textContent
(no innerHTML)."
```

---

### Task 18: Insert-widget menu

**Files:**
- Modify: `internal/glance/static/js/admin/app.js`

- [ ] **Step 1: Add the menu wiring**

Append inside the IIFE, before `})();`:

```javascript
  const insertBtn = document.getElementById('insert-widget-btn');
  let stubs = null;
  insertBtn.addEventListener('click', async () => {
    if (!stubs) {
      const r = await api('/widget-stubs');
      stubs = await r.json();
    }
    const names = Object.keys(stubs).sort();
    const name = window.prompt('Widget type to insert:\n' + names.join(', '));
    if (!name) return;
    const stub = stubs[name];
    if (!stub) { setStatus('no stub for "' + name + '"'); return; }

    const pos = editor.selectionStart;
    const before = editor.value.substring(0, pos);
    const lineStart = before.lastIndexOf('\n') + 1;
    const indent = (before.substring(lineStart).match(/^ */) || [''])[0];
    const indented = stub.split('\n').map((line, i) => i === 0 ? line : (line ? indent + line : line)).join('\n');
    editor.value = before + indented + editor.value.substring(pos);
    editor.selectionStart = editor.selectionEnd = pos + indented.length;
    editor.dispatchEvent(new Event('input'));
  });
```

- [ ] **Step 2: Manually verify**

In `/admin`: click "Insert widget…", enter `clock`, confirm a clock stub is inserted at the cursor with correct indent.

- [ ] **Step 3: Commit**

```bash
git add internal/glance/static/js/admin/app.js
git commit -m "feat(admin): insert-widget menu

Fetches widget-stubs once, prompts for a name (simple prompt() for MVP;
richer UI later), inserts the stub at the cursor with indent matching
the current line."
```

---

### Task 19: History tab UI + full-render preview

**Files:**
- Modify: `internal/glance/static/js/admin/app.js`

- [ ] **Step 1: Add history UI and full-render button using DOM APIs**

Append inside the IIFE, before `})();`:

```javascript
  const historyList = document.getElementById('history-list');

  async function loadHistory() {
    clearChildren(historyList);
    const r = await api('/history');
    if (!r.ok) {
      historyList.appendChild(el('div', null, 'failed to load history'));
      return;
    }
    const entries = await r.json();
    entries.forEach(entry => {
      const row = el('div', { class: 'admin-history-entry' });
      row.appendChild(el('strong', null, entry.Message));
      const meta = el('div', { class: 'meta' });
      meta.textContent = entry.Time + ' · ' + entry.Email + ' · ' + (entry.SHA || '').substring(0, 8);
      row.appendChild(meta);
      row.addEventListener('click', async () => {
        if (!confirm('Restore to commit ' + entry.SHA.substring(0, 8) + '?\n' + entry.Message)) return;
        const rr = await api('/history/' + entry.SHA + '/restore', { method: 'POST' });
        if (!rr.ok) { setStatus('restore failed'); return; }
        setStatus('restored ✓ reloading…');
        setTimeout(() => window.location.reload(), 800);
      });
      historyList.appendChild(row);
    });
  }

  // Reload history when the history mobile tab is shown, and on initial load.
  root.querySelectorAll('.admin-mobile-tabs button[data-tab="history"]').forEach(btn => {
    btn.addEventListener('click', loadHistory);
  });
  loadHistory();

  // Full-render preview button.
  const pvtabs = root.querySelectorAll('.admin-preview-tabs button');
  const previewIframe = document.getElementById('preview-iframe');
  const previewSkel = document.getElementById('preview-skeleton');
  pvtabs.forEach(b => {
    b.addEventListener('click', async () => {
      pvtabs.forEach(x => x.classList.remove('active'));
      b.classList.add('active');
      if (b.dataset.pvtab === 'skeleton') {
        previewIframe.classList.remove('visible');
        previewSkel.style.display = '';
      } else {
        previewSkel.style.display = 'none';
        setStatus('rendering full preview…');
        const r = await api('/preview', { method: 'POST', body: state.pendingContents, headers: { 'Content-Type': 'text/plain' } });
        if (!r.ok) {
          const body = await r.json().catch(() => ({}));
          setStatus('preview failed');
          setError(body.error || 'preview failed');
          return;
        }
        const { preview_id } = await r.json();
        previewIframe.src = baseURL + '/admin/preview/' + preview_id + '/';
        previewIframe.classList.add('visible');
        setStatus('preview ready');
      }
    });
  });
```

- [ ] **Step 2: Manually verify**

1. Make a save; commit appears in the History tab.
2. Click a history entry; confirm the file reverts and the page reloads.
3. Click "Full render"; iframe shows the actual dashboard rendered from the current editor buffer.

- [ ] **Step 3: Commit**

```bash
git add internal/glance/static/js/admin/app.js
git commit -m "feat(admin): history tab + full-render preview

History list built with createElement/textContent; no innerHTML with
dynamic content. Full-render posts the pending buffer to
/admin/api/preview and loads the returned id in an iframe."
```

---

### Task 20: Documentation

**Files:**
- Modify: `docs/configuration.md`

- [ ] **Step 1: Append the admin section to docs/configuration.md**

At the end of the file, append:

```markdown

## Admin web UI

An authenticated `/admin` endpoint that lets you edit `glance.yml` (and any
`!include`d files) in a browser, with live skeleton preview, on-demand full
render, and git-backed edit history.

### Prerequisites

- `git` on `$PATH` (used for edit history)
- At least one user configured under `auth.users` — the admin uses the same
  session cookie as the dashboard, stacked behind Cloudflare Access
- Cloudflare Access configured in front of Glance (or `GLANCE_ADMIN_DEV_BYPASS=1`
  for local development only)

### Configuration

```yaml
admin:
  enabled: true
  history-dir: ./.glance-history   # default; committed to a local-only git repo
  cloudflare-access:
    team-domain: example.cloudflareaccess.com
    audience: <your CF Access app AUD tag>
    allowed-emails:
      - you@example.com
```

Every `/admin/*` request must carry a valid `Cf-Access-Jwt-Assertion` header
AND a valid Glance session cookie. Failure at either layer returns 401 with
the response header `X-Admin-Auth-Failed: cloudflare-access|session`.

### Development bypass

Set `GLANCE_ADMIN_DEV_BYPASS=1` in the environment to skip CF Access
verification for local testing. Glance password is still required. A warning
is logged on every startup while this is set.

### History repo

Every save is committed to `admin.history-dir` (default `./.glance-history`,
relative to the config file). The commit author comes from the CF Access JWT
`email` / `name` claims; in dev-bypass mode it falls back to the Glance
username. Adding `/.glance-history/` to your outer repo's `.gitignore` is
recommended.
```

- [ ] **Step 2: Commit**

```bash
git add docs/configuration.md
git commit -m "docs(admin): document the admin web UI configuration

Covers prerequisites (git, auth.users, CF Access), YAML schema, the
dev bypass env var, and history repo semantics."
```

---

## Verification

After Task 20, run the full suite and a manual end-to-end smoke test.

### Automated

```bash
cd /Users/cory/glance
go build ./...
go test ./...
```

Expected: all tests pass, build succeeds.

### Manual end-to-end

1. Generate a secret key and password hash:
   ```bash
   SECRET=$(go run . secret:make)
   PWHASH=$(go run . password:hash changeme)
   echo "secret: $SECRET"
   echo "hash:   $PWHASH"
   ```

2. Edit `./glance.yml` — insert an admin stanza with the generated values:
   ```yaml
   server:
     port: 8081
   auth:
     secret-key: <paste $SECRET>
     users:
       cory:
         password-hash: '<paste $PWHASH>'
   admin:
     enabled: true
     cloudflare-access:
       team-domain: local.example
       audience: abc
       allowed-emails:
         - you@example.com
   ```

3. Start Glance with the dev bypass:
   ```bash
   GLANCE_ADMIN_DEV_BYPASS=1 go run . --config glance.yml
   ```
   Expected log line: `admin: enabled on /admin`.

4. Visit `http://localhost:8081/admin` in a browser. You'll be redirected to
   `/login` (session layer rejected). Log in as `cory` / `changeme`.

5. Confirm each flow:
   - Editor shows `glance.yml` contents.
   - Typing updates the skeleton preview live.
   - Introducing a typo surfaces a red error strip and disables Save.
   - Fixing the typo and saving shows `reloaded ✓`.
   - The change is visible when the dashboard tab is refreshed.
   - `Insert widget…` → `clock` inserts a stub at the cursor.
   - "Full render" tab shows the dashboard rendered from the current pending
     config in an iframe.
   - History tab lists the commits; clicking a prior one reverts the file.

6. Stop the server (Ctrl-C).

### Production setup (out of scope, referenced in spec)

After this plan ships, the remaining work to put `/admin` on the public
internet is:
1. Configure a Cloudflare Tunnel to your Glance instance.
2. Create a Cloudflare Access application fronting the tunnel.
3. Set `admin.cloudflare-access.{team-domain,audience,allowed-emails}` to
   match the Access app.
4. Unset `GLANCE_ADMIN_DEV_BYPASS`.
5. Confirm an unauthenticated request from outside CF is stopped at
   Cloudflare Access before it reaches Glance.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-04-19-webui-config-editor.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
