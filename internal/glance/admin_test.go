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
