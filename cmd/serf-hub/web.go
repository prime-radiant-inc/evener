package main

import (
	"encoding/json"
	"fmt"
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
	"primeradiant.com/serf/internal/appserver"
)

// WebServer wires routes, templates, and middleware.
type WebServer struct {
	cfg                 hubcore.WebConfig
	appTmpl             *template.Template
	sidebarTmpl         *template.Template
	workspaceTmpl       *template.Template
	threadTmpl          *template.Template
	spawnTmpl           *template.Template
	inputStripTmpl      *template.Template
	projectSettingsTmpl *template.Template
	settingsTmpls       map[string]*template.Template
	appRPC              *appserver.Server
	sources             *appsource.Registry
	startedAt           time.Time

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
	treeCache *hubcore.TreeCache
}

// sidebarTemplateFuncs supplies small helpers the sidebar template needs:
// integer math plus the terminal-state test used to fold completed subagent
// rows behind the "Completed (N)" disclosure.
var sidebarTemplateFuncs = template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"sub": func(a, b int) int { return a - b },
	// subagentDone reports whether a subagent's display state is terminal — it
	// has finished and no longer needs attention. Working states (running,
	// awaiting input, warning) are not done. The sidebar folds done subagents
	// behind a "Completed (N)" disclosure.
	"subagentDone": func(state string) bool {
		switch state {
		case "active", "awaiting", "warning", "errored":
			return false
		default:
			return true
		}
	},
}

// NewWebServer constructs the web server. Templates are parsed from embed.FS.
func NewWebServer(cfg hubcore.WebConfig) *WebServer {
	appTmpl := template.Must(template.New("app.html").Funcs(template.FuncMap{"assetv": assetVersionQuery}).ParseFS(templatesRoot(), "templates/app.html"))
	sidebarTmpl := template.Must(template.New("sidebar.html").Funcs(sidebarTemplateFuncs).ParseFS(templatesRoot(), "templates/partials/sidebar.html"))
	workspaceTmpl := template.Must(template.ParseFS(templatesRoot(),
		"templates/partials/workspace.html",
		"templates/partials/input_strip.html",
	))
	threadTmpl := template.Must(template.ParseFS(templatesRoot(),
		"templates/thread.html",
		"templates/partials/workspace.html",
		"templates/partials/input_strip.html",
	))
	spawnTmpl := template.Must(template.ParseFS(templatesRoot(),
		"templates/partials/spawn.html",
	))
	inputStripTmpl := template.Must(template.ParseFS(templatesRoot(),
		"templates/partials/input_strip.html",
	))
	projectSettingsTmpl := template.Must(template.ParseFS(templatesRoot(),
		"templates/partials/settings/project.html",
	))
	settingsSections := []string{"general", "theme", "transcript", "notifications", "providers", "agents", "launch-serf", "launch-codex", "inrepo", "plugins", "skills", "mcp", "hub", "storage", "credentials"}
	settingsTmpls := make(map[string]*template.Template, len(settingsSections))
	for _, sec := range settingsSections {
		files := []string{"templates/partials/settings.html"}
		if sec == "credentials" {
			files = append(files, "templates/partials/credentials.html")
		} else {
			files = append(files, "templates/partials/settings/"+sec+".html")
		}
		settingsTmpls[sec] = template.Must(template.ParseFS(templatesRoot(), files...))
	}
	sources := newHubSourceRegistry(cfg)
	if cfg.CodexLauncher == nil && len(cfg.CodexLaunches) > 0 {
		cfg.CodexLauncher = codexlaunch.NewCodexLauncher(cfg.CodexLaunches)
	}
	web := &WebServer{
		cfg: cfg, appTmpl: appTmpl, sidebarTmpl: sidebarTmpl,
		workspaceTmpl: workspaceTmpl, threadTmpl: threadTmpl, spawnTmpl: spawnTmpl, inputStripTmpl: inputStripTmpl,
		projectSettingsTmpl: projectSettingsTmpl,
		settingsTmpls:       settingsTmpls,
		sources:             sources,
		startedAt:           time.Now().UTC(),
		resumeLocks:         map[string]*sync.Mutex{},
		lastGoodThreads:     map[string][]appwire.Thread{},
		liveModels:          &modelsCache{},
		treeCache:           &hubcore.TreeCache{},
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
	mux.HandleFunc("/_partials/", s.handleInternalPartial)

	// Document panes — read-only file/markdown viewer framed by a side pane.
	mux.HandleFunc("/doc/file", s.handleDocFile)

	// Settings
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/settings/", s.handleSettings)

	// Credentials
	mux.HandleFunc("/credentials", s.handleCredentials)
	mux.HandleFunc("/_partials/credentials", s.handleCredentialsPartial)

	// API
	mux.HandleFunc("/api/spawn", s.handleApiSpawn)
	mux.HandleFunc("/api/models", s.handleApiModels)
	mux.HandleFunc("/api/dirs", s.handleApiDirs)
	mux.HandleFunc("/api/dirs/create", s.handleAPIDirCreate)
	mux.HandleFunc("/api/path/validate", s.handleAPIPathValidate)
	mux.HandleFunc("/api/git/head", s.handleApiGitHead)
	mux.HandleFunc("/api/search", s.handleApiSearch)
	mux.HandleFunc("/api/health", s.handleAPIHealth)
	mux.HandleFunc("/api/upgrade", s.handleAPIUpgrade)
	mux.HandleFunc("/_api/subagent-preview", s.handleSubagentPreview)
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
	raw, err := fs.ReadFile(assetsRoot(), "manifest.webmanifest")
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
	out, err := json.Marshal(manifest)
	if err != nil {
		http.Error(w, "manifest encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	// The token is per-browser-jar; never let a shared cache hold this response.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

// handleInternalPartial is the only route family that serves HTMX fragments.
// Keeping fragments under /_partials prevents direct navigation to app-shell
// internals that need sidebar scripts, client state, or a containing pane.
func (s *WebServer) handleInternalPartial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.NotFound(w, r)
		return
	}

	switch {
	case r.URL.Path == "/_partials/sidebar/project":
		s.handleSidebarProject(w, r)
	case r.URL.Path == "/_partials/sidebar":
		s.handleSidebar(w, r)
	case r.URL.Path == "/_partials/workspace/empty":
		s.handleWorkspaceEmpty(w, r)
	case r.URL.Path == "/_partials/workspace/spawn":
		s.handleWorkspaceSpawn(w, r)
	case strings.HasPrefix(r.URL.Path, "/_partials/s/"):
		s.handleSessionPartial(w, r)
	case strings.HasPrefix(r.URL.Path, "/_partials/settings"):
		section := strings.TrimPrefix(r.URL.Path, "/_partials/settings")
		section = strings.TrimPrefix(section, "/")
		if section == "" {
			section = "general"
		}
		s.renderSettingsPartial(w, r, section)
	default:
		http.NotFound(w, r)
	}
}

func (s *WebServer) handleSessionPartial(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/_partials/s/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	id = canonicalRouteID(id)
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}
	switch sub {
	case "workspace":
		s.renderWorkspacePartial(w, r, id)
	case "state":
		s.renderInputStrip(w, r, id)
	case "meta":
		s.renderWorkspaceMeta(w, r, id)
	case "details":
		s.renderDetailsPanel(w, r, id)
	case "tasks":
		s.renderSessionTasks(w, r, id)
	default:
		http.NotFound(w, r)
	}
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
	return err != nil || ref.SourceID == "local"
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

func (s *WebServer) handleSidebar(w http.ResponseWriter, r *http.Request) {
	metas, live := s.navigationTreeInputs(r.Context())
	tree := hubcore.BuildTree(metas, live, s.archiveDecisions())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.sidebarTmpl.ExecuteTemplate(w, "sidebar", tree); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleSidebarProject is the lazy-load endpoint for a single project's
// children. The default sidebar payload omits collapsed projects' session rows;
// sidebar.js fetches this fragment on first expand. It rebuilds just the named
// project (?key=<project name>) and renders the shared sidebarProjectChildren
// fragment. Auth + HX gating are inherited from handleInternalPartial.
func (s *WebServer) handleSidebarProject(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.NotFound(w, r)
		return
	}
	metas, live := s.navigationTreeInputs(r.Context())
	project, ok := hubcore.BuildProjectTree(metas, live, s.archiveDecisions(), key)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.sidebarTmpl.ExecuteTemplate(w, "sidebarProjectChildren", project); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) handleWorkspaceEmpty(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, `<div class="empty-state empty-state-workspace">
  <div class="welcome-wordmark">serf<span class="welcome-dot">.</span></div>
  <p class="empty-state-title">Watch your agents work.</p>
  <p class="empty-state-body">Spawn a session, or pick one from the sidebar. Every session stays live — jump back in any time.</p>
  <div class="empty-state-actions">
    <a class="btn btn-primary" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ New session</a>
    <button class="btn btn-ghost" type="button" data-search-trigger>Search <kbd>⌘K</kbd></button>
  </div>
</div>`)
}
