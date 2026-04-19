# Web UI Config Editor — Design

**Status:** Design approved, pending implementation plan
**Date:** 2026-04-19
**Scope:** One subsystem of a larger personal-fork roadmap (see *Out of scope* below for the full list).

## Context

Glance's config lives in `glance.yml` (with optional `!include` for multi-file setups). Today the only way to edit it is SSH-into-the-host-and-edit. For a personal deployment that lives behind Cloudflare Access and is used from mobile, that's untenable: you want to tweak a widget or add a feed from your phone on the way to work. This design adds an authenticated `/admin` editor with live preview and git-backed history.

This is the first of five planned subsystems for the fork; the others (Cloudflare hosting/auth, GitHub activity widget, LLM dashboard companion, Claude Code session monitor) are not in scope here but have informed decisions below (e.g., we assume CF Access is present).

## Goals

- Edit `glance.yml` and all `!include`d files from a web browser, usable on desktop and mobile.
- Validate before save; never leave the disk in an invalid state.
- Safety net: every save is a git commit; any prior state is one click away.
- Live skeleton preview while typing; full render on demand.
- No new runtime dependencies beyond a JWT verifier (`github.com/coreos/go-oidc/v3`) and a vendored `js-yaml` static file.
- Off by default; impossible to accidentally expose.

## Out of scope (deferred to future specs)

- CSS/assets editor
- Inline HTML/JS editor for `html`/`custom-api` widget bodies
- Multi-user permission levels (editor vs. viewer)
- Cloudflare Tunnel setup (ops, not code)
- Widget-level form UI (we chose raw YAML with live preview — cheaper, more flexible)

## Architecture

### Integration with existing Glance

New file: `internal/glance/admin.go` (+ `admin_test.go`). A few admin templates under `internal/glance/templates/` and admin assets under `internal/glance/static/js/admin/`, `internal/glance/static/css/admin.css`.

**Mount point:** `/admin/*` on the same `http.ServeMux` as the dashboard. Registered only if all three are true:
1. `admin.enabled: true` in YAML
2. `auth.users` has ≥1 user (Glance's existing auth configured)
3. `admin.cloudflare-access.*` configured **OR** `GLANCE_ADMIN_DEV_BYPASS=1` environment variable set

If any condition fails, `/admin` is not mounted and startup logs exactly which condition failed.

**Reused Glance primitives:**
- `parseYAMLIncludes(path)` (`config.go:242`) — returns contents + include set
- `newConfigFromYAML(contents []byte)` — validation
- `newApplication(config)` — used to construct sandboxed preview apps
- Existing fsnotify watcher in `serveApp` (`main.go:93`) — picks up our disk writes automatically; we do **not** trigger reloads ourselves
- Existing auth: `authMiddleware` + `session_token` cookie (`auth.go`)
- Existing bundle CSS via `staticFSHash`

**New code, by file:**
- `admin.go` — admin server, routes, save flow, preview registry
- `admin_cfaccess.go` — CF Access JWT verification + email allowlist
- `admin_history.go` — thin wrapper around `git init/add/commit/log/show/checkout` (shells out to system `git`)
- `admin_preview.go` — preview registry, TTL eviction, iframe-prefix router
- `templates/admin.html` — editor SPA shell
- `static/js/admin/app.js`, `static/js/admin/js-yaml.min.js` — client code, vendored YAML parser
- `static/css/admin.css` — minimal styles, imports theme vars from existing bundle

### Auth: defense-in-depth

Every `/admin/*` request passes two middlewares in order:

1. **CF Access JWT verification** — reads `Cf-Access-Jwt-Assertion` header, verifies signature against JWKS fetched from `https://{team-domain}/cdn-cgi/access/certs` (cached by `go-oidc`), verifies `aud` claim matches `admin.cloudflare-access.audience`, verifies `email` claim is present and is in `allowed-emails` (case-insensitive match). Missing header, invalid signature, wrong audience, missing email claim, or email not in allowlist → 401 with no body detail.
2. **Glance session cookie** — same check Glance already applies to authed pages. Missing/invalid → redirect to `/login`.

`GLANCE_ADMIN_DEV_BYPASS=1` short-circuits only the CF Access check; Glance password is always required. Named with `DEV` so it's obvious in process listings. Glance logs `WARNING: GLANCE_ADMIN_DEV_BYPASS is set — CF Access bypassed` at startup if the env var is present.

### URL surface

All routes require both auth layers. Paths are relative to `server.base-url` if set.

| Method | Path | Purpose |
|---|---|---|
| GET  | `/admin` | Editor SPA shell |
| GET  | `/admin/api/files` | List editable files (main + includes) with `mtime` and `size` |
| GET  | `/admin/api/files/{path}` | Raw file contents |
| PUT  | `/admin/api/files/{path}` | Write file (see *Save flow*) |
| POST | `/admin/api/validate` | Dry-run validation, no disk write |
| POST | `/admin/api/preview` | Create full-render preview, returns `{preview-id}` |
| GET  | `/admin/preview/{preview-id}/*` | Iframe source — strips prefix, forwards to sandboxed app mux |
| GET  | `/admin/api/history` | Last 50 commits from the history repo |
| GET  | `/admin/api/history/{sha}/diff` | Diff of a commit vs. its parent |
| POST | `/admin/api/history/{sha}/restore` | Restore files at SHA; recorded as a new commit |
| GET  | `/admin/api/config-generation` | Small integer incremented on each successful reload; used by client to confirm fsnotify saw the write |

`{path}` is URL-encoded and must resolve (after cleaning) to a file in the include set. Anything outside → 403.

### Save flow

1. Client `PUT /admin/api/files/{path}` with new contents in body.
2. Server:
   a. Verify `{path}` is in the current include set.
   b. Build a **virtual file set**: current on-disk contents of every other file in the set, plus the pending body for `{path}`.
   c. Run `parseYAMLIncludes` equivalent against the virtual set, then `newConfigFromYAML` on the combined bytes.
   d. On validation error → `400` with `{errors: [{path, message}]}`. No disk write.
   e. On success:
      1. Write the new contents into the history repo's mirror of `{path}`, `git add`, `git commit -m "edit {path} · {ISO8601} · {user-email}"`. If any of these fail → return `500`; no change to history, no change to real path.
      2. Write the new contents to the real path. If this fails → `git reset --hard HEAD~1` on the history repo to keep history and reality in sync, return `500`.
      3. Return `200` with the new `config-generation` value.
3. Glance's fsnotify watcher sees the real-path write and reloads within ~100ms. Client polls `/admin/api/config-generation` for 2s to confirm and reports "Reloaded ✓" or "Saved (waiting for reload)".

### Preview flows

**Live skeleton (in-browser, always on while typing):**
Client-side only. `js-yaml` parses pending YAML; a small renderer draws columns/widgets as labeled boxes. Never hits the server. Instantaneous, free, structurally accurate.

**Full render preview (button-triggered):**
1. Client `POST /admin/api/preview` with pending YAML body.
2. Server: `newConfigFromYAML` → `newApplication`. Store in a preview registry keyed by random 16-byte `preview-id`, TTL 5 min, last-access resets TTL. Max 3 live previews per session; LRU evict.
3. Client renders `<iframe src="/admin/preview/{preview-id}/">`.
4. Server handler for `/admin/preview/{preview-id}/*`: look up preview app, strip prefix, call preview app's mux.

Preview apps are scoped to the creating session cookie — another logged-in admin cannot see them. They do not inherit the admin middleware: requests under `/admin/preview/` only require the session cookie match, not CF Access (since they are already inside the admin shell, which did require CF Access).

Widgets in preview mode fetch external services the same as the live app — this is deliberate (WYSIWYG), and acceptable given rate-limited sources have their own caches.

### Git history

- Location: `./.glance-history/` relative to the config file (overridable via `admin.history-dir`).
- On startup, if the history dir doesn't exist: `git init`, copy current config files in, commit as `initial · {ISO8601}`.
- On every save: mirror edited files into the history dir, `git add {changed files}`, `git commit -m "edit {path} · {ISO8601} · {email}"`. Commit author: `{display-name} <{email}>` where `email` is the CF Access JWT `email` claim and `display-name` is the JWT `name` claim if present, else the part of the email before `@`. When `GLANCE_ADMIN_DEV_BYPASS=1` is active there is no JWT; author becomes `{glance-username} <admin@glance.local>`.
- Restore: `git checkout {sha} -- .`, copy files out to their real paths (tripping fsnotify), commit as `restore {sha[:8]} · {ISO8601} · {email}` so the timeline stays append-only.
- Shells out to system `git`. Refuses to mount admin if `git` not on `PATH`.
- `.gitignore` generated inside config dir on first run adding `.glance-history/` so users don't accidentally commit the history repo into their outer config repo.

### Config schema (added to `config-fields.go`)

```yaml
admin:
  enabled: false                          # default false; must be explicit
  history-dir: ./.glance-history           # optional override
  cloudflare-access:
    team-domain: example.cloudflareaccess.com
    audience: <CF Access app AUD tag>
    allowed-emails:
      - you@example.com
```

`admin.enabled` default is `false`. `cloudflare-access` fields are required when `enabled: true` unless `GLANCE_ADMIN_DEV_BYPASS=1`.

## Client UI

**Desktop:** three-pane layout.
- Left sidebar: file list (main + each include), current highlighted, unsaved-change dot.
- Center: editor — enhanced `<textarea>`, monospace, tab→2sp, shift-tab dedent, Ctrl/Cmd-S save, visible error strip above with YAML-path-based messages. No CodeMirror/Monaco for MVP; upgrade path is a later iteration without API changes.
- Right: tabs for **Preview** (live skeleton) / **Full render** (iframe, button-triggered) / **History**.

**Mobile:** same three zones collapsed into top-level tabs **Edit · Preview · History**. File list becomes a dropdown at the top of Edit tab.

**Insert-widget menu:** dropdown listing every widget type. The list and its YAML stubs live in a new `admin_widget_stubs.go` alongside the widget implementations so adding a widget keeps the editor honest (compile-time list, not a runtime reflection). Selecting inserts the stub at the cursor, matching the surrounding indent. Disproportionately useful on mobile.

**History tab:** reverse-chrono list of commits with timestamp, author, message, changed files. Click row → side-by-side diff modal. "Restore this state" button with confirm dialog.

## Tests

In `admin_test.go`:
- CF Access middleware: rejects missing header, invalid signature, wrong audience, email not in allowlist; accepts valid.
- Session middleware: existing Glance auth tests cover this; one integration test confirms both middlewares stack correctly.
- `GLANCE_ADMIN_DEV_BYPASS`: unit test that CF check is skipped, session still enforced.
- Save roundtrip: known-good config → 200 + git commit with expected author/message + file on disk matches body.
- Save rejection: known-bad config → 400 + no disk write + no git commit.
- Restore: commit a state, change the file, restore — asserts on-disk contents match the committed state and a new commit was created.
- Preview: `POST /admin/api/preview` returns an id; `GET /admin/preview/{id}/` serves HTML; TTL eviction removes stale entries; session-scoping rejects other-session access.
- Include traversal: attempting to save a path outside the include set returns 403.

## Risks and mitigations

1. **`newApplication` side effects.** Preview requires constructing a parallel app safely. Needs a skim to confirm no global state mutation; small refactor if not. *Mitigation:* scoped in Phase 0 of the plan.
2. **Watcher feedback loop.** Writing the file triggers fsnotify, which could race with a rapid second save. Glance already coalesces; single writes are fine. *Mitigation:* serialize writes behind a per-file mutex.
3. **Client/server YAML parser divergence.** `js-yaml` ≈ `yaml.v3` but not identical on anchors/custom tags. *Mitigation:* server is source of truth for validation; skeleton preview is structural only, explicitly not a contract.
4. **CF Access JWKS rotation.** First request after a key rotation triggers a refetch; ~50ms latency spike, not a failure. `go-oidc` handles this correctly.
5. **Git absence.** If system `git` is missing, admin refuses to mount with a clear message. No fallback to in-memory history — partial safety is worse than none.
6. **Preview resource exhaustion.** Many previews × many widgets could balloon memory. *Mitigation:* cap at 3 per session, 5 min TTL, LRU eviction.

## Verification

End-to-end happy path:
1. Configure `auth.users` and `admin.*` in `glance.yml`; set `GLANCE_ADMIN_DEV_BYPASS=1` for local testing.
2. Start Glance; confirm `admin enabled on /admin` in logs.
3. Navigate to `http://localhost:8081/admin`; log in with Glance credentials.
4. Edit `glance.yml` in the editor; confirm live skeleton preview updates.
5. Introduce a typo; confirm red error strip appears, save is blocked.
6. Fix the typo; save. Confirm "Saved ✓ Reloaded" state.
7. Refresh the dashboard tab; confirm the change is live.
8. Open History tab; confirm the commit appears with the right author and message.
9. Click "Restore this state" on the initial commit; confirm config reverts and a new restore-commit is recorded.
10. Click "Render full"; confirm the iframe shows the actual dashboard rendered from the current pending YAML with real widget data.

Unit tests (`go test ./internal/glance/... -run TestAdmin`) cover the middleware, save flow, history, preview, and traversal tests listed above.

Production path (after this spec):
- Configure CF Access app in Cloudflare dashboard, set `audience` + `allowed-emails` in config.
- Expose Glance via Cloudflare Tunnel.
- Test: unauthenticated request from outside CF is rejected at CF's edge; authenticated user hits Glance login; after Glance login, `/admin` is reachable.

## Files touched

- `internal/glance/admin.go` — new
- `internal/glance/admin_cfaccess.go` — new
- `internal/glance/admin_history.go` — new
- `internal/glance/admin_preview.go` — new
- `internal/glance/admin_test.go` — new
- `internal/glance/templates/admin.html` — new
- `internal/glance/static/js/admin/app.js` — new
- `internal/glance/static/js/admin/js-yaml.min.js` — new (vendored)
- `internal/glance/static/css/admin.css` — new
- `internal/glance/config-fields.go` — add `admin:` block
- `internal/glance/main.go` — pass live `*application` pointer to admin server; mount admin routes
- `go.mod` / `go.sum` — add `github.com/coreos/go-oidc/v3`
- `docs/configuration.md` — document the `admin:` block (appended, not rewritten)
