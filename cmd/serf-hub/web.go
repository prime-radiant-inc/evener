package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/httpsec"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/internal/hubapi"
	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/rendezvous"
)

// WebConfig is everything the web server needs.
type WebConfig struct {
	HubAddr             string
	AuthToken           string // capability token gating every non-exempt route
	HubStateRoot        string // root of hub-level state; defaults to $HOME/.serf
	RunDir              string // run directory where rendezvous files live
	PastIndexPath       string // path to the SQLite past-index DB, for display in settings
	Roster              *Roster
	Past                *PastIndex
	Spawner             Spawner                // optional; nil disables spawn
	Models              []modelDescriptor      // available models for the spawn chip
	PastPerPage         int                    // results per page for /past; defaults to 50 when zero
	StateDir            string                 // root of the projects/<sha> state directory; needed for ForkSession
	CredsStore          *credentials.Store     // credentials store; passed to auth controller
	PluginDirs          []string               // explicit plugin dirs; when empty, default to ~/.config/serf/plugins/*
	MCPConfigPath       string                 // MCP config file path; when empty, default to ~/.config/serf/mcp.json
	ProviderConfig      *providerconfig.Config // instance-to-tag mapping; nil when providers.toml absent (env path)
	ProvidersConfigPath string                 // path to providers.toml; forwarded to the auth controller
	CodexSources        []appsource.CodexSourceConfig
	CodexLaunches       []codexlaunch.CodexLaunchConfig
	CodexLauncher       *codexlaunch.CodexLauncher
}

// Spawner forks a serf serve subprocess and waits for its rendezvous file to appear.
// Returns the discovered Entry on success.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error)
	Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error)
}

// WebServer wires routes, templates, and middleware.
type WebServer struct {
	cfg                 WebConfig
	appTmpl             *template.Template
	sidebarTmpl         *template.Template
	workspaceTmpl       *template.Template
	spawnTmpl           *template.Template
	inputStripTmpl      *template.Template
	credsTmpl           *template.Template
	projectSettingsTmpl *template.Template
	settingsTmpls       map[string]*template.Template
	appRPC              *appserver.Server
	sources             *appsource.Registry
	startedAt           time.Time

	resumeMu    sync.Mutex
	resumeLocks map[string]*sync.Mutex // sessionID -> per-session lock
}

// NewWebServer constructs the web server. Templates are parsed from embed.FS.
func NewWebServer(cfg WebConfig) *WebServer {
	appTmpl := template.Must(template.ParseFS(templatesFS, "templates/app.html"))
	sidebarTmpl := template.Must(template.ParseFS(templatesFS, "templates/partials/sidebar.html"))
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
	credsTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/credentials.html",
	))
	projectSettingsTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/settings/project.html",
	))
	settingsSections := []string{"general", "theme", "notifications", "providers", "agents", "launch-serf", "launch-codex", "inrepo", "plugins", "skills", "mcp", "hub", "storage"}
	settingsTmpls := make(map[string]*template.Template, len(settingsSections))
	for _, sec := range settingsSections {
		settingsTmpls[sec] = template.Must(template.ParseFS(templatesFS,
			"templates/partials/settings.html",
			"templates/partials/settings/"+sec+".html",
		))
	}
	sources := newHubSourceRegistry(cfg)
	if cfg.CodexLauncher == nil && len(cfg.CodexLaunches) > 0 {
		cfg.CodexLauncher = codexlaunch.NewCodexLauncher(cfg.CodexLaunches)
	}
	web := &WebServer{
		cfg: cfg, appTmpl: appTmpl, sidebarTmpl: sidebarTmpl,
		workspaceTmpl: workspaceTmpl, spawnTmpl: spawnTmpl, inputStripTmpl: inputStripTmpl,
		credsTmpl:           credsTmpl,
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

	mux.HandleFunc("/auth", HandleAuth(s.cfg.AuthToken))

	auth := AuthGuard(s.cfg.AuthToken)
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

func (s *WebServer) handleApiSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	var resp searchResponse
	if s.cfg.Roster != nil {
		live := s.cfg.Roster.List()
		sort.SliceStable(live, func(i, j int) bool {
			return liveEntryWithPastLess(live[i], live[j], s.cfg.Past)
		})
		for _, le := range live {
			if le.SessionID == "" {
				continue
			}
			title := liveTitle(le.SessionID, le, s.cfg.Past)
			if q == "" || strings.Contains(strings.ToLower(le.SessionID), q) || strings.Contains(strings.ToLower(title), q) {
				resp.Live = append(resp.Live, searchResult{
					ID:      le.SessionID,
					Title:   title,
					State:   normalizeState(le.Status),
					Project: filepath.Base(le.WorkingDir),
					Age:     "now",
				})
			}
		}
	}
	if s.cfg.Past != nil {
		// Empty query → most-recent N. Substring match otherwise.
		results := s.cfg.Past.Search(q, 20, 0)
		for _, e := range results {
			resp.Past = append(resp.Past, searchResult{
				ID:      e.Meta.ID,
				Title:   searchPastTitle(e),
				State:   "ended",
				Project: filepath.Base(e.Meta.EnvInfo.WorkingDir),
				Age:     ageString(e.Meta.UpdatedAt),
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeAPIJSON(w, status, hubapi.ErrorResponse{Error: msg})
}

func writeAPIWireError(w http.ResponseWriter, fallbackStatus int, err error) {
	wire, ok := wireErrorFromError(err)
	if !ok {
		writeAPIError(w, fallbackStatus, err.Error())
		return
	}
	writeAPIJSON(w, statusForWireError(wire, fallbackStatus), hubapi.ErrorResponse{
		Error:         wire.Message,
		Code:          wire.Code,
		SerfErrorInfo: serfErrorInfoFromData(wire.Data),
	})
}

func wireErrorFromError(err error) (appwire.WireError, bool) {
	var wire appwire.WireError
	if errors.As(err, &wire) {
		return wire, true
	}
	return appwire.WireError{}, false
}

func statusForWireError(wire appwire.WireError, fallback int) int {
	switch wire.Code {
	case appwire.CodeInvalidParams, appwire.CodeInvalidRequest:
		return http.StatusBadRequest
	case appwire.CodeMethodNotFound:
		return http.StatusNotFound
	case appwire.CodeConflict:
		return http.StatusConflict
	case appwire.CodeUnavailable:
		return http.StatusServiceUnavailable
	case appwire.CodeInternalError:
		return http.StatusInternalServerError
	default:
		return fallback
	}
}

func serfErrorInfoFromData(data any) string {
	switch v := data.(type) {
	case appwire.ErrorData:
		return string(v.SerfErrorInfo)
	case map[string]any:
		if info, ok := v["serfErrorInfo"].(string); ok {
			return info
		}
	}
	return ""
}

func (s *WebServer) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	writeAPIJSON(w, http.StatusOK, hubapi.HealthResponse{
		Version:   buildinfo.Version(),
		StartedAt: s.startedAt,
		HubAddr:   s.cfg.HubAddr,
		RunDir:    s.cfg.RunDir,
		StateGlob: s.apiStateGlob(),
		Capabilities: hubapi.HealthCapabilities{
			Tree:             true,
			TranscriptFollow: true,
			SpawnSchema:      true,
			Spawn:            s.cfg.Spawner != nil || len(s.cfg.CodexSources) > 0 || len(s.cfg.CodexLaunches) > 0 || s.cfg.CodexLauncher != nil,
			Fork:             true,
			RemoteSources:    len(s.cfg.CodexSources) > 0,
		},
	})
}

func (s *WebServer) apiStateGlob() string {
	if s.cfg.Past == nil {
		return ""
	}
	return s.cfg.Past.stateGlob
}

func (s *WebServer) handleAPISpawnSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	writeAPIJSON(w, http.StatusOK, hubapi.SpawnSchema{Fields: []hubapi.SpawnField{
		{Name: "prompt", Type: "text"},
		{Name: "harness", Type: "enum", Values: launchHarnessIDs(s.cfg)},
		{Name: "working_dir", Type: "path"},
		{Name: "model", Type: "model"},
		{Name: "agent", Type: "string"},
		{Name: "reasoning_effort", Type: "enum", Values: []string{"low", "medium", "high"}},
	}})
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

func warningPayload(raw json.RawMessage) map[string]any {
	message := warningMessage(raw)
	payload := map[string]any{"message": message}
	var params struct {
		Source string `json:"source"`
		Title  string `json:"title"`
		Hint   string `json:"hint"`
	}
	if json.Unmarshal(raw, &params) == nil {
		if params.Source != "" {
			payload["source"] = params.Source
		}
		if params.Title != "" {
			payload["title"] = params.Title
		}
		if params.Hint != "" {
			payload["hint"] = params.Hint
		}
	}
	addDiagnosticDefaults(payload, message)
	return payload
}

func addDiagnosticDefaults(payload map[string]any, message string) {
	info := diagnostic.Classify(message)
	if _, ok := payload["source"]; !ok {
		payload["source"] = string(info.Source)
	}
	if _, ok := payload["title"]; !ok {
		payload["title"] = info.Title
	}
	if _, ok := payload["hint"]; !ok {
		payload["hint"] = info.Hint
	}
}

func warningMessage(raw json.RawMessage) string {
	var params struct {
		Message string `json:"message"`
		Warning any    `json:"warning"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return string(raw)
	}
	if strings.TrimSpace(params.Message) != "" {
		return params.Message
	}
	switch warning := params.Warning.(type) {
	case string:
		return warning
	case map[string]any:
		if message, ok := warning["message"].(string); ok {
			return message
		}
	}
	return string(raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *WebServer) handleAPIClear(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !s.isLive(id) {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	if err := s.ensureSessionActionAvailable(id, "clear"); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	refText := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, refText, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	resp, err := source.ClearThread(r.Context(), appwire.ThreadClearParams{Ref: refText})
	if err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	outRefText := resp.Ref
	if outRefText == "" {
		outRefText = resp.Thread.Serf.Ref
	}
	ref, err := hubapi.ParseRef(outRefText)
	if err != nil {
		ref = hubapi.LocalRef(resp.Thread.ID)
	}
	writeAPIJSON(w, http.StatusOK, hubapi.RefResponse{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
	})
}

func (s *WebServer) handleAPIModel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !s.isLive(id) {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, model := splitProviderModel(body.Model)
	if model == "" {
		model = body.Model
	}
	if err := s.ensureSessionActionAvailable(id, "model"); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	if err := source.SetThreadModel(r.Context(), appwire.ThreadModelSetParams{Ref: ref, ModelProvider: provider, Model: model}); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *WebServer) handleSidebar(w http.ResponseWriter, r *http.Request) {
	metas, live := s.navigationTreeInputs(r.Context())
	tree := BuildTree(metas, live)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.sidebarTmpl.ExecuteTemplate(w, "sidebar", tree); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) handleWorkspaceEmpty(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<div class="empty-state empty-state-workspace">
  <p class="empty-state-title">Welcome to serf-hub</p>
  <p class="empty-state-body">Spawn a session to start working with an agent, or search across live and past sessions. The hub keeps every session alive in the sidebar — pick one to jump in.</p>
  <div class="empty-state-actions">
    <a class="btn btn-secondary" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ New session</a>
    <button class="btn btn-ghost" type="button" data-search-trigger>⌘K search</button>
  </div>
</div>`)
}

// handleApiDirs returns directories matching a path prefix for the directory autocomplete.
func (s *WebServer) handleApiDirs(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		prefix = os.Getenv("HOME")
	}
	// Expand ~ to home.
	if strings.HasPrefix(prefix, "~/") || prefix == "~" {
		prefix = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(prefix, "~"))
	}
	// Reject traversal; preserve trailing slash so the listDir/filter logic
	// below still distinguishes "list dir contents" from "filter siblings".
	cleaned, err := sanitizeDirPrefix(prefix)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`)) //nolint:errcheck
		return
	}
	prefix = cleaned

	// If prefix ends with "/", list contents of that directory.
	// Otherwise, list contents of the parent and filter by basename prefix.
	var listDir, filter string
	if strings.HasSuffix(prefix, "/") || prefix == "" {
		listDir = prefix
		if listDir == "" {
			listDir = "/"
		}
		filter = ""
	} else {
		listDir = filepath.Dir(prefix)
		filter = filepath.Base(prefix)
	}

	entries, err := os.ReadDir(listDir)
	if err != nil {
		// Return empty list rather than error — UI shows no matches.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`)) //nolint:errcheck
		return
	}

	type result struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		IsGit bool   `json:"is_git"`
	}
	var results []result
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") && filter == "" {
			continue // hide dotfiles unless user typed a dot
		}
		if filter != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(filter)) {
			continue
		}
		full := filepath.Join(listDir, name)
		isGit := false
		if _, gerr := os.Stat(filepath.Join(full, ".git")); gerr == nil {
			isGit = true
		}
		results = append(results, result{Path: full, Name: name, IsGit: isGit})
		if len(results) >= 30 {
			break
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results}) //nolint:errcheck
}

func (s *WebServer) handleAPIPathValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	resp := validateLaunchPath(appwire.PathValidateParams{
		Path: r.URL.Query().Get("path"),
		Kind: r.URL.Query().Get("kind"),
	})
	writeAPIJSON(w, http.StatusOK, resp)
}

// gitHeadBranch runs `git rev-parse --abbrev-ref HEAD` in dir and returns
// the branch name. In detached HEAD state it falls back to the short SHA.
func gitHeadBranch(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		// Detached HEAD — return short SHA instead.
		out2, err2 := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
		if err2 != nil {
			return branch, nil
		}
		branch = strings.TrimSpace(string(out2))
	}
	return branch, nil
}

// handleApiGitHead returns the current git HEAD branch name for a given cwd.
// Query param: cwd=<absolute path>. Returns {"branch": "<name>"} or {"branch": ""} on error.
func (s *WebServer) handleApiGitHead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	branch := ""
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
		if _, err := os.Stat(cwd); err == nil {
			if out, err := gitHeadBranch(cwd); err == nil {
				branch = out
			}
		}
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"branch": branch})
}
