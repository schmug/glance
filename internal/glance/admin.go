package glance

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

	saveMu           sync.Mutex
	configGeneration atomic.Int64

	// sessionKeyFn returns a stable key identifying the current session. In
	// production it reads the session cookie; tests override it.
	sessionKeyFn func(r *http.Request) string
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
	mux.HandleFunc("GET "+prefix+"/api/files", a.middleware(a.handleListFiles))
	mux.HandleFunc("GET "+prefix+"/api/files/{path...}", a.middleware(a.handleReadFile))
	mux.HandleFunc("POST "+prefix+"/api/validate", a.middleware(a.handleValidate))
	mux.HandleFunc("PUT "+prefix+"/api/files/{path...}", a.middleware(a.handleWriteFile))
	mux.HandleFunc("GET "+prefix+"/api/config-generation", a.middleware(a.handleConfigGeneration))
}

func (a *adminServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Placeholder — replaced with the editor shell in Task 15.
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("admin ok"))
}

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
	//   - Editing an include: body is a YAML fragment and cannot be
	//     validated as a standalone config. Skip strict validation;
	//     Glance's fsnotify watcher will log and reject a bad reload while
	//     keeping the previous config live.
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

// Stub types populated in later tasks.
type previewRegistry struct{}
