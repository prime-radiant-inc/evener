package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
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
	spawnTmpl           *template.Template
	inputStripTmpl      *template.Template
	projectSettingsTmpl *template.Template
	settingsTmpls       map[string]*template.Template
	appRPC              *appserver.Server
	sources             *appsource.Registry
	startedAt           time.Time

	resumeMu    sync.Mutex
	resumeLocks map[string]*sync.Mutex // sessionID -> per-session lock
}

// sidebarTemplateFuncs supplies small helpers the sidebar template needs:
// integer math to fold subagent rows ("+N subagents"), and a "dict" builder
// so a reusable project sub-template can receive both the TreeProject and its
// tier's expand hint in one call.
var sidebarTemplateFuncs = template.FuncMap{
	"add":  func(a, b int) int { return a + b },
	"sub":  func(a, b int) int { return a - b },
	"dict": templateDict,
}

// templateDict builds a map from alternating key/value args for passing
// multiple values into a sub-template ({{template "x" (dict "P" . "Expanded" $e)}}).
func templateDict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments (%d)", len(pairs))
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d is not a string", i)
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// NewWebServer constructs the web server. Templates are parsed from embed.FS.
func NewWebServer(cfg hubcore.WebConfig) *WebServer {
	appTmpl := template.Must(template.ParseFS(templatesFS, "templates/app.html"))
	sidebarTmpl := template.Must(template.New("sidebar.html").Funcs(sidebarTemplateFuncs).ParseFS(templatesFS, "templates/partials/sidebar.html"))
	workspaceTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/workspace.html",
		"templates/partials/input_strip.html",
	))
	spawnTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/spawn.html",
	))
	inputStripTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/input_strip.html",
	))
	projectSettingsTmpl := template.Must(template.ParseFS(templatesFS,
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
		settingsTmpls[sec] = template.Must(template.ParseFS(templatesFS, files...))
	}
	sources := newHubSourceRegistry(cfg)
	if cfg.CodexLauncher == nil && len(cfg.CodexLaunches) > 0 {
		cfg.CodexLauncher = codexlaunch.NewCodexLauncher(cfg.CodexLaunches)
	}
	web := &WebServer{
		cfg: cfg, appTmpl: appTmpl, sidebarTmpl: sidebarTmpl,
		workspaceTmpl: workspaceTmpl, spawnTmpl: spawnTmpl, inputStripTmpl: inputStripTmpl,
		projectSettingsTmpl: projectSettingsTmpl,
		settingsTmpls:       settingsTmpls,
		sources:             sources,
		startedAt:           time.Now().UTC(),
		resumeLocks:         map[string]*sync.Mutex{},
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
func (s *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()

	// Assets
	sub, _ := fs.Sub(assetsFS, "assets")
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))

	// App-wire RPC
	mux.HandleFunc("/rpc", s.appRPC.ServeWebSocket)

	// Pages
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/s/", s.handleSession)
	mux.HandleFunc("/new", s.handleIndex)
	mux.HandleFunc("/_partials/", s.handleInternalPartial)

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
	mux.HandleFunc("/api/path/validate", s.handleAPIPathValidate)
	mux.HandleFunc("/api/git/head", s.handleApiGitHead)
	mux.HandleFunc("/api/search", s.handleApiSearch)
	mux.HandleFunc("/api/health", s.handleAPIHealth)
	mux.HandleFunc("/api/tree", s.handleAPITree)
	mux.HandleFunc("/api/spawn-schema", s.handleAPISpawnSchema)
	mux.HandleFunc("/api/sessions/", s.handleAPISession)

	mux.HandleFunc("/auth", hubedge.HandleAuth(s.cfg.AuthToken))

	auth := hubedge.AuthGuard(s.cfg.AuthToken)
	return auth(httpsec.CSPMiddleware(mux))
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

func projectKey(name string) string {
	if name == "" {
		return "project"
	}
	return strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(name)
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
	tree := hubcore.BuildTree(metas, live, map[hubcore.ArchiveKey]bool{})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.sidebarTmpl.ExecuteTemplate(w, "sidebar", tree); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) handleWorkspaceEmpty(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, `<div class="empty-state empty-state-workspace">
  <p class="empty-state-title">Welcome to serf-hub</p>
  <p class="empty-state-body">Spawn a session to start working with an agent, or search across live and past sessions. The hub keeps every session alive in the sidebar — pick one to jump in.</p>
  <div class="empty-state-actions">
    <a class="btn btn-secondary" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ New session</a>
    <button class="btn btn-ghost" type="button" data-search-trigger>⌘K search</button>
  </div>
</div>`)
}
