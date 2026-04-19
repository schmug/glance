package glance

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
