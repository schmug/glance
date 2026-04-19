package glance

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
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

	req := httptest.NewRequest("GET", "/admin/api/files"+main, nil)
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

	req := httptest.NewRequest("GET", "/admin/api/files"+forbidden, nil)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rw.Code)
	}
	if strings.Contains(rw.Body.String(), "secret") {
		t.Fatalf("body must not leak file content")
	}
}

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
	req := httptest.NewRequest("PUT", "/admin/api/files"+cfgPath, strings.NewReader(newBody))
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
	req := httptest.NewRequest("PUT", "/admin/api/files"+cfgPath, strings.NewReader(bad))
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
