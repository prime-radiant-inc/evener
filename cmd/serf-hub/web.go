package main

import (
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/httpsec"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubedge"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/internal/appserver"
)

// WebServer wires routes, templates, and middleware.
type WebServer struct {
	cfg        hubcore.WebConfig
	appTmpl    *template.Template
	threadTmpl *template.Template
	appRPC     *appserver.Server
	sources    *appsource.Registry
	startedAt  time.Time

	resumeMu    sync.Mutex
	resumeLocks map[string]*sync.Mutex // sessionID -> per-session lock

	// lastGoodThreads retains each remote source's most recent successful
	// ListThreads result so a transient list failure doesn't blank that
	// source's sessions from the sidebar (which renders a snapshot).
	lastGoodMu      sync.Mutex
	lastGoodThreads map[string][]appwire.Thread // sourceID -> last successful list
	// liveModels caches raw live /models listings for this server; per-server
	// so another WebServer (different provider config) never shares entries.
	liveModels *modelsCache
	// treeCache memoizes the /api/tree BuildTree+attention-summary computation
	// by inputs-version + 30s time bucket (see hubcore.TreeCache).
	treeCache  *hubcore.TreeCache
	manifestFS fs.FS
}

// inputStripTemplateFuncs supplies the input-status partial's formatting
// helpers: formatWorkMillis renders WS2's accumulated work time compactly;
// formatTokenCount mirrors web_format.formatTokenCount but takes the int64
// token counts carried on hubapi.Usage/appwire.SerfUsage.
var inputStripTemplateFuncs = template.FuncMap{
	"formatWorkMillis": formatWorkMillis,
	"formatTokenCount": func(n int64) string { return formatTokenCount(int(n)) },
}

var manifestMarshal = json.Marshal

// NewWebServer constructs the web server. Templates are parsed from embed.FS.
func NewWebServer(cfg hubcore.WebConfig) *WebServer {
	appTmpl := template.Must(template.New("app.html").Funcs(template.FuncMap{"assetv": assetVersionQuery}).ParseFS(templatesRoot(), "templates/app.html"))
	threadTmpl := template.Must(template.New("thread.html").Funcs(inputStripTemplateFuncs).ParseFS(templatesRoot(),
		"templates/thread.html",
		"templates/partials/workspace.html",
		"templates/partials/input_strip.html",
	))
	sources := newHubSourceRegistry(cfg)
	if cfg.CodexLauncher == nil && len(cfg.CodexLaunches) > 0 {
		cfg.CodexLauncher = codexlaunch.NewCodexLauncher(cfg.CodexLaunches)
	}
	web := &WebServer{
		cfg: cfg, appTmpl: appTmpl,
		threadTmpl:      threadTmpl,
		sources:         sources,
		startedAt:       time.Now().UTC(),
		resumeLocks:     map[string]*sync.Mutex{},
		lastGoodThreads: map[string][]appwire.Thread{},
		liveModels:      &modelsCache{},
		treeCache:       &hubcore.TreeCache{},
		manifestFS:      assetsRoot(),
	}
	web.appRPC = newHubAppServer(cfg, sources)
	return web
}

// lockForSession returns (creating if necessary) the per-session mutex for
// serializing concurrent resume requests on the same session_id.
func (s *WebServer) lockForSession(sessionID string) *sync.Mutex {
	s.resumeMu.Lock()
	defer s.resumeMu.Unlock()
	m, ok := s.resumeLocks[sessionID]
	if !ok {
		m = &sync.Mutex{}
		s.resumeLocks[sessionID] = m
	}
	return m
}

// Handler returns the http.Handler with all routes wired and the security
// guard middleware applied.
// validAssetPath short-circuits asset requests whose cleaned path fs.ValidPath
// rejects (e.g. an invalid-UTF-8 byte) to a 404. A bare http.FileServer maps
// the resulting fs.ErrInvalid to a 500; for a static file server a malformed
// path is a not-found, not a server fault.
func validAssetPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()

	// Assets — served from disk when SERF_HUB_ASSETS_DIR is set (dev), else embed.
	assetHandler := http.StripPrefix("/assets/", validAssetPath(http.FileServer(http.FS(assetsRoot()))))
	if devAssetsDir() != "" {
		// In the on-disk dev mode, disable caching so CSS/JS edits take effect on
		// reload without the browser serving a stale heuristically-cached copy.
		assetHandler = noStore(assetHandler)
	}
	mux.Handle("/assets/", assetHandler)

	// Rewritten SPA's hashed Vite output — always registered regardless of
	// SERF_HUB_WEB, mirroring /assets/ above (same auth-guard wrapping below;
	// hashed filenames are immutable, so aggressive caching is safe).
	mux.Handle("/webassets/", webassetsHandler(distFS()))

	// PWA manifest — served dynamically (not as a static asset) so start_url can
	// carry the auth token. A home-screen launch on iOS gets its own cookie jar,
	// separate from the browser that authorized the hub, so it must re-auth via
	// the token in start_url or it lands on the 401 wall. Auth-gated, so only an
	// already authorized browser can read the token.
	mux.HandleFunc("/manifest.webmanifest", s.handleManifest)

	// App-wire RPC
	mux.HandleFunc("/rpc", s.appRPC.ServeWebSocket)

	// Pages
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/s/", s.handleSession)
	mux.HandleFunc("/thread/", s.handleThreadDocument)
	mux.HandleFunc("/new", s.handleIndex)

	// Document panes — read-only file/markdown viewer framed by a side pane.
	mux.HandleFunc("/doc/file", s.handleDocFile)
	mux.HandleFunc("/doc/image", s.handleDocImage)

	// Settings
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/settings/", s.handleSettings)

	// Credentials
	mux.HandleFunc("/credentials", s.handleCredentials)

	// API
	mux.HandleFunc("/api/spawn", s.handleApiSpawn)
	mux.HandleFunc("/api/models", s.handleApiModels)
	mux.HandleFunc("/api/dirs/create", s.handleAPIDirCreate)
	mux.HandleFunc("/api/path/validate", s.handleAPIPathValidate)
	mux.HandleFunc("/api/git/head", s.handleApiGitHead)
	mux.HandleFunc("/api/search", s.handleApiSearch)
	mux.HandleFunc("/api/health", s.handleAPIHealth)
	mux.HandleFunc("/api/upgrade", s.handleAPIUpgrade)
	mux.HandleFunc("/api/tree/project", s.handleAPITreeProject)
	mux.HandleFunc("/api/tree", s.handleAPITree)
	mux.HandleFunc("/api/archive", s.handleAPIArchive)
	mux.HandleFunc("/api/favorite", s.handleAPIFavorite)
	mux.HandleFunc("/api/project/delete", s.handleAPIProjectDelete)
	mux.HandleFunc("/api/spawn-schema", s.handleAPISpawnSchema)
	mux.HandleFunc("/api/sessions/", s.handleAPISession)

	mux.HandleFunc("/auth", hubedge.HandleAuth(s.cfg.AuthToken))

	auth := hubedge.AuthGuard(s.cfg.AuthToken)
	// Optional opt-in (SERF_RECORD_HTTP) inbound-request recorder for fuzz-corpus
	// harvesting; identity middleware when unset, so the stack is unchanged.
	record := newHTTPRequestRecorder(s.cfg.HubStateRoot)
	return record(auth(httpsec.CSPMiddleware(mux)))
}

func (s *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if newWebEnabled() {
		serveSPAIndex(w, r, distFS())
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/new" {
		http.NotFound(w, r)
		return
	}
	workspaceURL := "/_partials/workspace/empty"
	if r.URL.Path == "/new" {
		workspaceURL = "/_partials/workspace/spawn"
		// Forward optional pre-fill params:
		//   ?dir=<path> — sidebar's per-project "+" button uses this.
		//   ?prompt=<text> — the palette's /spawn command seeds the textarea.
		params := url.Values{}
		if dir := strings.TrimSpace(r.URL.Query().Get("dir")); dir != "" {
			params.Set("dir", dir)
		}
		if prompt := r.URL.Query().Get("prompt"); prompt != "" {
			params.Set("prompt", prompt)
		}
		if encoded := params.Encode(); encoded != "" {
			workspaceURL += "?" + encoded
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": workspaceURL}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleManifest serves the PWA manifest with the auth token injected into
// start_url. The manifest file on disk is the template; we only rewrite
// start_url so a standalone (home-screen) launch self-authenticates into its
// own cookie jar instead of hitting the auth wall. When the guard is disabled
// (no token) start_url stays "/".
func (s *WebServer) handleManifest(w http.ResponseWriter, r *http.Request) {
	manifestFS := s.manifestFS
	if manifestFS == nil {
		manifestFS = assetsRoot()
	}
	raw, err := fs.ReadFile(manifestFS, "manifest.webmanifest")
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		http.Error(w, "manifest malformed", http.StatusInternalServerError)
		return
	}
	if token := s.cfg.AuthToken; token != "" {
		manifest["start_url"] = "/auth?token=" + url.QueryEscape(token) + "&next=" + url.QueryEscape("/")
	}
	out, err := manifestMarshal(manifest)
	if err != nil {
		http.Error(w, "manifest encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	// The token is per-browser-jar; never let a shared cache hold this response.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

func localAppRef(threadID string) string {
	return appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
}

func appRefFromRouteID(id string) string {
	if ref, err := appwire.ParseRef(id); err == nil {
		return ref.String()
	}
	return localAppRef(id)
}

func isLocalRouteID(id string) bool {
	ref, err := appwire.ParseRef(id)
	if err != nil {
		return identifier.ValidateSessionID(id) == nil
	}
	return ref.SourceID == "local" && identifier.ValidateSessionID(ref.ThreadID) == nil
}

func canonicalRouteID(id string) string {
	ref, err := appwire.ParseRef(id)
	if err != nil || ref.SourceID != "local" {
		return id
	}
	return ref.ThreadID
}

func splitProviderModel(raw string) (string, string) {
	provider, model, ok := strings.Cut(strings.TrimSpace(raw), "/")
	if !ok {
		return "", strings.TrimSpace(raw)
	}
	return provider, model
}
