package app

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	ui "github.com/sbrw-evc/VLAN-Analizer/web"
)

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, Object{"detail": message})
}
func body(r *http.Request, w http.ResponseWriter, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func sameOrigin(r *http.Request, expected string) bool {
	origin := r.Header.Get("Origin")
	if expected != "" {
		return origin == expected
	}
	u, e := url.Parse(origin)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host
}
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", webApp())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, Object{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		repo, errText := a.repo, a.workerError
		a.mu.RUnlock()
		if repo == nil || errText != "" || repo.Ping(r.Context()) != nil {
			fail(w, 503, "setup required or dependencies unavailable")
			return
		}
		respond(w, 200, Object{"status": "ready"})
	})
	mux.HandleFunc("GET /api/setup/status", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, a.Status()) })
	mux.HandleFunc("POST /api/setup/complete", a.setupComplete)
	mux.HandleFunc("POST /api/setup/test/{source}", a.setupTest)
	mux.HandleFunc("POST /auth/local", a.localLogin)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("GET /auth/entra", a.entraStart)
	mux.HandleFunc("GET /auth/entra/callback", a.entraCallback)
	mux.HandleFunc("GET /api/me", a.protected(false, false, func(w http.ResponseWriter, r *http.Request, u Session) { respond(w, 200, u) }))
	mux.HandleFunc("GET /api/dashboard", a.protected(false, false, func(w http.ResponseWriter, r *http.Request, u Session) {
		data, e := a.snapshot(r.Context())
		if e != nil {
			fail(w, 503, "storage unavailable")
			return
		}
		respond(w, 200, data)
	}))
	mux.HandleFunc("POST /api/checks/run", a.protected(true, true, func(w http.ResponseWriter, r *http.Request, u Session) {
		a.notify()
		respond(w, 202, Object{"queued": true})
	}))
	mux.HandleFunc("POST /api/{source}/sync", a.protected(true, true, func(w http.ResponseWriter, r *http.Request, u Session) {
		data, e := a.syncSource(r.Context(), r.PathValue("source"))
		if e != nil {
			fail(w, 502, "source import failed; check connection and permissions")
			return
		}
		a.notify()
		respond(w, 200, data)
	}))
	mux.HandleFunc("GET /api/history", a.protected(false, false, func(w http.ResponseWriter, r *http.Request, u Session) {
		hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
		if hours < 1 {
			hours = 24
		}
		if hours > 2160 {
			fail(w, 422, "invalid history range")
			return
		}
		a.mu.RLock()
		repo := a.repo
		a.mu.RUnlock()
		data, e := repo.History(r.Context(), r.URL.Query().Get("site_id"), r.URL.Query().Get("interface"), hours)
		if e != nil {
			fail(w, 503, "history unavailable")
			return
		}
		respond(w, 200, data)
	}))
	mux.HandleFunc("POST /api/sites", a.protected(true, true, a.saveSite))
	mux.HandleFunc("PUT /api/sites/{id}", a.protected(true, true, a.saveSite))
	mux.HandleFunc("DELETE /api/sites/{id}", a.protected(true, true, a.deleteSite))
	mux.HandleFunc("PUT /api/users", a.protected(true, true, a.saveUser))
	mux.HandleFunc("GET /api/settings/connections", a.protected(true, false, func(w http.ResponseWriter, r *http.Request, u Session) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		respond(w, 200, a.connections.Public(a.config.PublicURL))
	}))
	mux.HandleFunc("PUT /api/settings/connections/{source}", a.protected(true, true, a.connection))
	mux.HandleFunc("POST /api/settings/connections/{source}/test", a.protected(true, true, a.connection))
	mux.HandleFunc("GET /api/settings/monitoring", a.protected(true, false, func(w http.ResponseWriter, r *http.Request, u Session) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		respond(w, 200, a.scan)
	}))
	mux.HandleFunc("PUT /api/settings/monitoring", a.protected(true, true, a.monitoring))
	mux.HandleFunc("GET /api/network/vlans", a.protected(true, false, a.networkVLANs))
	mux.HandleFunc("POST /api/network/vlans", a.protected(true, true, a.createVLANs))
	mux.HandleFunc("PUT /api/network/vlans/{name}", a.protected(true, true, a.updateVLAN))
	mux.HandleFunc("DELETE /api/network/vlans/{name}", a.protected(true, true, a.deleteVLAN))
	mux.HandleFunc("POST /api/network/vlans/{name}/{state}", a.protected(true, true, a.vlanState))
	mux.HandleFunc("GET /api/integrations/zabbix", a.zabbix)
	mux.HandleFunc("GET /api/integrations/zabbix/discovery", a.zabbix)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}

// webApp serves the built React app: existing files as they are and index.html for client-side routes.
func webApp() http.Handler {
	dist, _ := fs.Sub(ui.Assets, "dist")
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "api" || strings.HasPrefix(name, "api/") || strings.HasPrefix(name, "auth/") {
			fail(w, 404, "not found")
			return
		}
		if info, e := fs.Stat(dist, name); name != "" && name != "index.html" && e == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		data, e := fs.ReadFile(dist, "index.html")
		if e != nil {
			http.Error(w, "the web app is not built; run npm run build in web/", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
}
func (a *App) protected(admin, write bool, fn func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		auth, config, entra := a.auth, a.config, a.connections.Entra.Enabled
		a.mu.RUnlock()
		if auth == nil {
			fail(w, 503, "complete setup or wait for dependencies")
			return
		}
		u, e := auth.User(r.Context(), r, entra)
		if e != nil {
			fail(w, 401, "sign in required")
			return
		}
		if admin && u.Role != "admin" {
			fail(w, 403, "administrator role required")
			return
		}
		if write && !sameOrigin(r, config.PublicURL) {
			fail(w, 403, "origin must match application URL")
			return
		}
		fn(w, r, u)
	}
}
func (a *App) setupAllowed(w http.ResponseWriter, r *http.Request) bool {
	a.mu.RLock()
	configured := a.config != nil
	a.mu.RUnlock()
	if configured {
		fail(w, 409, "initial setup is already complete")
		return false
	}
	if !sameOrigin(r, "") {
		fail(w, 403, "same-origin setup request required")
		return false
	}
	return true
}
func (a *App) setupComplete(w http.ResponseWriter, r *http.Request) {
	if !a.setupAllowed(w, r) {
		return
	}
	var form SetupForm
	if body(r, w, &form) != nil {
		fail(w, 422, "invalid setup fields")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if e := a.Complete(ctx, form); e != nil {
		fail(w, 502, e.Error())
		return
	}
	_ = a.activate(ctx)
	respond(w, 200, Object{"ok": true, "ready": a.Status()["ready"]})
}
func (a *App) setupTest(w http.ResponseWriter, r *http.Request) {
	if !a.setupAllowed(w, r) {
		return
	}
	switch r.PathValue("source") {
	case "openbao":
		var c BaoConfig
		if body(r, w, &c) != nil {
			fail(w, 422, "invalid OpenBao settings")
			return
		}
		form, _, e := a.resolveSetup(SetupForm{Bao: c})
		if e == nil {
			e = testVault(r.Context(), form.Bao)
		}
		if e != nil {
			fail(w, 502, "OpenBao connection failed; verify URL, seal status and credentials")
			return
		}
	case "clickhouse":
		var c CHConfig
		if body(r, w, &c) != nil {
			fail(w, 422, "invalid ClickHouse settings")
			return
		}
		form, _, e := a.resolveSetup(SetupForm{ClickHouse: c})
		if e == nil {
			e = NewClickHouse(form.ClickHouse).Ping(r.Context())
		}
		if e != nil {
			fail(w, 502, "ClickHouse connection failed")
			return
		}
	default:
		fail(w, 404, "unknown service")
		return
	}
	respond(w, 200, Object{"ok": true, "message": "Connection verified"})
}
func testVault(ctx context.Context, c BaoConfig) error {
	if c.Mode == "token" {
		var out Object
		_, e := requestJSON(ctx, httpClient(15*time.Second), "GET", c.URL+"/v1/sys/mounts", map[string]string{"X-Vault-Token": c.Token}, nil, &out)
		return e
	}
	var out Object
	_, e := NewVault(c).Read(ctx, "app", &out)
	return e
}
func (a *App) localLogin(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	auth, config := a.auth, a.config
	a.mu.RUnlock()
	if auth == nil {
		fail(w, 503, "application is not configured")
		return
	}
	if !sameOrigin(r, config.PublicURL) {
		fail(w, 403, "invalid origin")
		return
	}
	if !auth.AllowLogin(remoteIP(r)) {
		fail(w, 429, "too many attempts")
		return
	}
	var form struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if body(r, w, &form) != nil || len(form.Password) > 1024 {
		fail(w, 422, "invalid credentials")
		return
	}
	token, e := auth.Local(r.Context(), form.Username, form.Password)
	if e != nil {
		fail(w, 401, "invalid credentials or authentication unavailable")
		return
	}
	sessionCookie(w, "session", token, config.PublicURL, 28800)
	respond(w, 200, Object{"ok": true})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	config := a.config
	a.mu.RUnlock()
	if config == nil || !sameOrigin(r, config.PublicURL) {
		fail(w, 403, "invalid origin")
		return
	}
	sessionCookie(w, "session", "", config.PublicURL, -1)
	respond(w, 200, Object{"ok": true})
}
func (a *App) entraStart(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	auth, c, config := a.auth, a.connections.Entra, a.config
	a.mu.RUnlock()
	if auth == nil || config == nil {
		fail(w, 503, "setup required")
		return
	}
	target, cookie, e := auth.EntraStart(r.Context(), c, config.PublicURL)
	if e != nil {
		fail(w, 503, "Entra ID is unavailable")
		return
	}
	sessionCookie(w, "oidc", cookie, config.PublicURL, 600)
	http.Redirect(w, r, target, 302)
}
func (a *App) entraCallback(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	auth, c, config := a.auth, a.connections.Entra, a.config
	a.mu.RUnlock()
	if auth == nil || !c.Enabled {
		fail(w, 401, "Entra is unavailable")
		return
	}
	cookie, e := r.Cookie("oidc")
	if e != nil {
		fail(w, 401, "OIDC flow expired")
		return
	}
	token, e := auth.EntraCallback(r.Context(), c, config.PublicURL, r.URL.Query().Get("code"), r.URL.Query().Get("state"), cookie.Value)
	if e != nil {
		fail(w, 401, "Entra sign-in failed; verify credentials and app roles")
		return
	}
	sessionCookie(w, "oidc", "", config.PublicURL, -1)
	sessionCookie(w, "session", token, config.PublicURL, 28800)
	http.Redirect(w, r, "/", 302)
}
func (a *App) saveSite(w http.ResponseWriter, r *http.Request, u Session) {
	var form struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Enabled bool   `json:"enabled"`
	}
	if body(r, w, &form) != nil || len(form.Name) == 0 || len(form.Name) > 120 || validateURL(form.URL) != nil {
		fail(w, 422, "invalid website name or URL")
		return
	}
	a.importMu.Lock()
	defer a.importMu.Unlock()
	a.mu.RLock()
	repo := a.repo
	a.mu.RUnlock()
	sites, e := repo.Sites(r.Context())
	if e != nil {
		fail(w, 503, "storage unavailable")
		return
	}
	s := Site{ID: randomKey(16), Name: form.Name, URL: form.URL, Source: "manual", SourceEnabled: 1}
	status := 201
	if r.PathValue("id") != "" {
		found := false
		for _, old := range sites {
			if old.ID == r.PathValue("id") {
				s = old
				found = true
				break
			}
		}
		if !found {
			fail(w, 404, "site not found")
			return
		}
		if s.Source != "manual" && s.URL != form.URL {
			fail(w, 409, "change imported URLs in their source")
			return
		}
		status = 200
	} else if len(sites) >= 3000 {
		fail(w, 409, "site limit reached")
		return
	}
	s.Name = form.Name
	s.URL = form.URL
	s.Enabled = 0
	if form.Enabled {
		s.Enabled = 1
	}
	if e = repo.SaveSite(r.Context(), s); e != nil {
		fail(w, 503, "site save failed")
		return
	}
	a.notify()
	respond(w, status, s)
}
func (a *App) deleteSite(w http.ResponseWriter, r *http.Request, u Session) {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	a.mu.RLock()
	repo := a.repo
	a.mu.RUnlock()
	sites, e := repo.Sites(r.Context())
	if e != nil {
		fail(w, 503, "storage unavailable")
		return
	}
	for _, s := range sites {
		if s.ID == r.PathValue("id") {
			if s.Source != "manual" {
				fail(w, 409, "disable imported sites or remove them in the source")
				return
			}
			s.Deleted = 1
			if e = repo.SaveSite(r.Context(), s); e != nil {
				fail(w, 503, "site delete failed")
				return
			}
			respond(w, 200, Object{"ok": true})
			return
		}
	}
	fail(w, 404, "site not found")
}
func (a *App) saveUser(w http.ResponseWriter, r *http.Request, u Session) {
	var form struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
	}
	if body(r, w, &form) != nil {
		fail(w, 422, "invalid user fields")
		return
	}
	user, e := newUser(form.Username, form.Password, form.Role, form.Disabled)
	if e != nil {
		fail(w, 422, e.Error())
		return
	}
	a.mu.RLock()
	vault := a.vault
	a.mu.RUnlock()
	if e = vault.Write(r.Context(), "users/"+form.Username, user, nil); e != nil {
		fail(w, 503, "user save failed")
		return
	}
	respond(w, 200, Object{"username": form.Username, "role": user.Role, "disabled": user.Disabled})
}
func (a *App) monitoring(w http.ResponseWriter, r *http.Request, u Session) {
	var scan ScanConfig
	if body(r, w, &scan) != nil || scan.Validate() != nil {
		fail(w, 422, "invalid scan parameters")
		return
	}
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.mu.RLock()
	vault := a.vault
	a.mu.RUnlock()
	if e := vault.Write(r.Context(), "connections/monitoring", scan, nil); e != nil {
		fail(w, 503, "settings save failed")
		return
	}
	a.mu.Lock()
	a.scan = scan
	a.mu.Unlock()
	a.notify()
	respond(w, 200, scan)
}
func (a *App) connection(w http.ResponseWriter, r *http.Request, u Session) {
	source := r.PathValue("source")
	a.importMu.Lock()
	defer a.importMu.Unlock()
	a.mu.RLock()
	c, vault, origin := a.connections, a.vault, a.config.PublicURL
	a.mu.RUnlock()
	var input Object
	if body(r, w, &input) != nil {
		fail(w, 422, "invalid settings")
		return
	}
	delete(input, "token_set")
	delete(input, "password_set")
	delete(input, "client_secret_set")
	delete(input, "redirect_uri")
	var target any
	var value any
	switch source {
	case "gatus":
		target = &c.Gatus
	case "kuma":
		target = &c.Kuma
	case "entra":
		target = &c.Entra
	default:
		fail(w, 404, "unknown source")
		return
	}
	data, _ := json.Marshal(input)
	if e := json.Unmarshal(data, target); e != nil {
		fail(w, 422, "invalid integration fields")
		return
	}
	var e error
	switch source {
	case "gatus":
		if c.Gatus.Enabled && (c.Gatus.URL == "") == (c.Gatus.Path == "") {
			e = errors.New("choose Gatus API or YAML")
		}
		if c.Gatus.URL != "" {
			e = serverURL(c.Gatus.URL)
		}
		value = c.Gatus
	case "kuma":
		if c.Kuma.Enabled && (c.Kuma.Host == "" || c.Kuma.Password == "" || c.Kuma.Instance == "" || c.Kuma.Port < 1 || c.Kuma.Port > 65535) {
			e = errors.New("invalid Kuma connection")
		}
		value = c.Kuma
	case "entra":
		e = c.Entra.Validate()
		value = c.Entra
	}
	if e != nil {
		fail(w, 422, e.Error())
		return
	}
	if strings.HasSuffix(r.URL.Path, "/test") {
		var importer Importer
		switch source {
		case "gatus":
			if c.Gatus.Enabled {
				importer = GatusImporter{c.Gatus}
			}
		case "kuma":
			if c.Kuma.Enabled {
				importer = KumaImporter{c.Kuma}
			}
		case "entra":
			if !c.Entra.Enabled {
				fail(w, 422, "enable Entra to test it")
				return
			}
			a.mu.RLock()
			auth := a.auth
			a.mu.RUnlock()
			if _, e = auth.oidc(r.Context(), c.Entra); e != nil {
				fail(w, 502, "Entra discovery failed")
				return
			}
			respond(w, 200, Object{"ok": true, "message": "Tenant discovery verified; credentials and roles are checked at sign-in"})
			return
		}
		if importer == nil {
			fail(w, 422, "enable the integration to test it")
			return
		}
		sites, warnings, err := importer.Fetch(r.Context())
		if err != nil {
			fail(w, 502, "connection test failed; verify credentials and source schema")
			return
		}
		respond(w, 200, Object{"ok": true, "sites": len(sites), "warnings": warnings})
		return
	}
	if e = vault.Write(r.Context(), "connections/"+source, value, nil); e != nil {
		fail(w, 503, "connection save failed")
		return
	}
	a.mu.Lock()
	a.connections = c
	a.mu.Unlock()
	a.notify()
	respond(w, 200, c.Public(origin))
}
func (a *App) zabbix(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	vault := a.vault
	a.mu.RUnlock()
	if vault == nil {
		fail(w, 503, "not configured")
		return
	}
	var secrets Object
	if _, e := vault.Read(r.Context(), "app", &secrets); e != nil {
		fail(w, 503, "secrets unavailable")
		return
	}
	expected := text(secrets["zabbix_token"])
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if expected == "" || !hmac.Equal([]byte(expected), []byte(supplied)) {
		fail(w, 401, "invalid integration token")
		return
	}
	snapshot, e := a.snapshot(r.Context())
	if e != nil {
		fail(w, 503, "snapshot unavailable")
		return
	}
	items := zabbixItems(snapshot)
	if strings.HasSuffix(r.URL.Path, "/discovery") {
		out := []Object{}
		for _, i := range items {
			vlan := "untagged-or-unknown"
			if p, ok := i["vlan_id"].(*int); ok && p != nil {
				vlan = fmt.Sprint(*p)
			}
			out = append(out, Object{"{#SITE_ID}": i["site_id"], "{#SITE}": i["site"], "{#INTERFACE}": i["interface"], "{#VLAN}": vlan})
		}
		respond(w, 200, out)
		return
	}
	respond(w, 200, Object{"generated_at": timestamp(), "items": items})
}
