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

// Stub types populated in later tasks.
type gitHistory struct{}
type previewRegistry struct{}
