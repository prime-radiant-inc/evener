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

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/editorurl"
	"primeradiant.com/serf/cmd/serf-hub/internal/httpsec"
	"primeradiant.com/serf/cmd/serf-hub/internal/mcpstatus"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/frontmatter"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/internal/hubapi"
	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
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

func (s *WebServer) handleAPITree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	metas, live := s.navigationTreeInputs(r.Context())
	tree := BuildTree(metas, live)
	resp := hubapi.TreeResponse{
		GeneratedAt: time.Now().UTC(),
		Sources:     s.apiTreeSources(),
	}
	for _, n := range tree.Live {
		if !treeNodeCanActLive(n) {
			continue
		}
		resp.Live = append(resp.Live, s.apiTreeNode("live", "", n, true))
	}
	seenProjectRefs := map[string]bool{}
	projectIndexes := map[string]int{}
	for _, p := range tree.Projects {
		key := projectKey(p.Name)
		ap := hubapi.TreeProject{
			Key:         key,
			Name:        p.Name,
			WorkingDir:  p.WorkingDir,
			RollupState: p.RollupState,
		}
		for _, n := range p.Sessions {
			ap.Sessions = append(ap.Sessions, s.apiTreeNode("project", key, n, treeNodeCanActLive(n) && s.isLive(n.ID)))
			seenProjectRefs[n.ID] = true
		}
		projectIndexes[key] = len(resp.Projects)
		resp.Projects = append(resp.Projects, ap)
	}
	for _, le := range live {
		if le.SessionID == "" || seenProjectRefs[le.SessionID] {
			continue
		}
		project := filepath.Base(le.WorkingDir)
		if project == "" || project == "." {
			project = "(no project)"
		}
		key := projectKey(project)
		node := TreeNode{
			ID:        le.SessionID,
			Title:     liveTitle(le.SessionID, le, s.cfg.Past),
			Project:   project,
			State:     normalizeState(le.Status),
			Kind:      "session",
			CreatedAt: le.StartedAt,
			UpdatedAt: le.StartedAt,
			Age:       ageString(le.StartedAt),
		}
		apiNode := s.apiTreeNode("project", key, node, true)
		if idx, ok := projectIndexes[key]; ok {
			p := &resp.Projects[idx]
			p.Sessions = append(p.Sessions, apiNode)
			if attentionRank(node.State) > attentionRank(p.RollupState) {
				p.RollupState = node.State
			}
			continue
		}
		projectIndexes[key] = len(resp.Projects)
		resp.Projects = append(resp.Projects, hubapi.TreeProject{
			Key:         key,
			Name:        project,
			WorkingDir:  le.WorkingDir,
			RollupState: node.State,
			Sessions:    []hubapi.TreeNode{apiNode},
		})
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (s *WebServer) navigationTreeInputs(ctx context.Context) ([]agent.SessionMeta, []LiveEntry) {
	var live []LiveEntry
	if s.cfg.Roster != nil {
		live = s.cfg.Roster.List()
	}
	var metas []agent.SessionMeta
	if s.cfg.Past != nil {
		metas = s.cfg.Past.AllMetas()
	}
	for _, thread := range s.remoteTreeThreads(ctx) {
		meta, entry, ok := appThreadTreeEntries(thread)
		if !ok {
			continue
		}
		metas = append(metas, meta)
		if appThreadTreeLive(thread) {
			live = append(live, entry)
		}
	}
	return metas, live
}

func (s *WebServer) remoteTreeThreads(ctx context.Context) []appwire.Thread {
	if s.sources == nil {
		return nil
	}
	s.ensureManagedCodexSources(ctx)
	var threads []appwire.Thread
	for _, source := range s.sources.All() {
		if source.ID() == "local" {
			continue
		}
		resp, err := source.ListThreads(ctx, appwire.ThreadListParams{IncludeSubagents: true})
		if err != nil {
			continue
		}
		for _, thread := range resp.Data {
			sourceID := threadListSourceID(source.ID(), thread)
			thread.Source = sourceID
			if thread.Serf.Ref == "" {
				threadID := firstNonEmpty(thread.ID, thread.SessionID)
				if threadID != "" {
					thread.Serf.Ref = appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
				}
			}
			threads = append(threads, thread)
		}
	}
	return threads
}

func (s *WebServer) ensureManagedCodexSources(ctx context.Context) {
	_ = ensureManagedCodexSources(ctx, s.cfg, s.sources, appwire.ThreadListParams{})
}

func appThreadTreeEntries(thread appwire.Thread) (agent.SessionMeta, LiveEntry, bool) {
	ref, ok := appThreadTreeRef(thread)
	if !ok {
		return agent.SessionMeta{}, LiveEntry{}, false
	}
	refText := ref.String()
	title := firstNonEmpty(thread.Name, thread.Preview, thread.SessionID, thread.ID, refText)
	createdAt := unixTime(thread.CreatedAt)
	updatedAt := unixTime(thread.UpdatedAt)
	meta := agent.SessionMeta{
		ID:             refText,
		ProfileID:      ref.SourceID,
		Model:          thread.ModelProvider,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		OriginalPrompt: title,
		EnvInfo: agent.EnvironmentInfo{
			WorkingDir: thread.CWD,
		},
	}
	if thread.GitInfo != nil {
		meta.EnvInfo.GitBranch = thread.GitInfo.Branch
		meta.EnvInfo.GitOriginURL = thread.GitInfo.OriginURL
	}
	entry := LiveEntry{
		Entry: rendezvous.Entry{
			SourceID:   ref.SourceID,
			ThreadID:   ref.ThreadID,
			SessionID:  refText,
			WorkingDir: thread.CWD,
			Model:      thread.ModelProvider,
			StartedAt:  orderCreatedAt(createdAt, updatedAt),
		},
		SessionID: refText,
		Status:    thread.Status.Type,
	}
	return meta, entry, true
}

func appThreadTreeRef(thread appwire.Thread) (appwire.Ref, bool) {
	if thread.Serf.Ref != "" {
		if ref, err := appwire.ParseRef(thread.Serf.Ref); err == nil {
			return ref, true
		}
	}
	sourceID := strings.TrimSpace(thread.Source)
	threadID := firstNonEmpty(thread.ID, thread.SessionID)
	if sourceID == "" || threadID == "" {
		return appwire.Ref{}, false
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}, true
}

func appThreadTreeLive(thread appwire.Thread) bool {
	switch thread.Status.Type {
	case appwire.ThreadStatusClosed, appwire.ThreadStatusNotLoaded:
		return false
	default:
		return true
	}
}

func (s *WebServer) apiTreeSources() []hubapi.Source {
	sources := []hubapi.Source{{
		ID:     "local",
		Label:  "this host",
		Kind:   "local",
		Online: true,
	}}
	if s.sources == nil {
		return sources
	}
	for _, source := range s.sources.All() {
		if source.ID() == "local" {
			continue
		}
		sources = append(sources, hubapi.Source{
			ID:     source.ID(),
			Label:  source.ID(),
			Kind:   "appwire",
			Online: true,
		})
	}
	return sources
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

func hubRefFromAppThread(thread appwire.Thread) hubapi.Ref {
	refText := thread.Serf.Ref
	if refText == "" {
		refText = appwire.Ref{SourceID: thread.Source, ThreadID: thread.ID}.String()
	}
	ref, err := hubapi.ParseRef(refText)
	if err != nil {
		return hubapi.LocalRef(thread.ID)
	}
	return ref
}

func hubCapabilitiesFromAppwire(caps appwire.ThreadCapabilities) hubapi.SessionCapabilities {
	return hubapi.SessionCapabilities{
		Send:        caps.Send,
		Steer:       caps.Steer,
		Interrupt:   caps.Interrupt,
		Compact:     caps.Compact,
		Clear:       caps.Clear,
		Fork:        caps.ForkFromTurn,
		Shutdown:    caps.Shutdown,
		ChangeModel: caps.ChangeModel,
		Queue:       caps.Queue,
	}
}

func hubDetailFromAppThread(thread appwire.Thread) hubapi.SessionDetail {
	ref := hubRefFromAppThread(thread)
	state := normalizeState(thread.Status.Type)
	if state == "" {
		state = "idle"
	}
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	if title == "" {
		title = thread.SessionID
	}
	project := filepath.Base(thread.CWD)
	if project == "" || project == "." {
		project = "(no project)"
	}
	live := state != "ended" && state != "closed"
	detail := hubapi.SessionDetail{
		Ref:              ref.String(),
		HostID:           ref.HostID,
		SessionID:        ref.SessionID,
		Title:            title,
		State:            state,
		Live:             live,
		Project:          project,
		WorkingDir:       thread.CWD,
		Model:            thread.ModelProvider,
		Profile:          thread.Serf.Profile,
		TurnCount:        completedTurnCount(thread.Turns),
		ActiveTurnID:     activeTurnIDFromAppwireThread(thread),
		ContextPressure:  thread.Serf.ContextPressure,
		ContextUsed:      thread.Serf.ContextUsed,
		ContextWindow:    thread.Serf.ContextWindow,
		ContextRemaining: thread.Serf.ContextRemaining,
		Capabilities:     hubCapabilitiesFromAppwire(thread.Serf.Capabilities),
	}
	if detail.SessionID == "" {
		detail.SessionID = thread.ID
	}
	return detail
}

func (s *WebServer) isLive(sessionID string) bool {
	if !isLocalRouteID(sessionID) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, appRefFromRouteID(sessionID), "")
		return err == nil
	}
	if s.cfg.Roster == nil {
		return false
	}
	_, ok := s.cfg.Roster.Find(sessionID)
	return ok
}

func treeNodeCanActLive(n TreeNode) bool {
	return normalizeState(n.State) != "ended"
}

func (s *WebServer) apiTreeNode(scope, projectKey string, n TreeNode, live bool) hubapi.TreeNode {
	ref := hubRefFromTreeNodeID(n.ID)
	refText := ref.String()
	rowID := scope + ":" + refText
	if projectKey != "" {
		rowID = scope + ":" + projectKey + ":" + refText
	}
	out := hubapi.TreeNode{
		RowID:     rowID,
		Ref:       refText,
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
		Title:     n.Title,
		Project:   n.Project,
		State:     n.State,
		Kind:      n.Kind,
		Live:      live,
		UpdatedAt: n.UpdatedAt,
		Age:       n.Age,
	}
	if le, ok := s.liveEntry(n.ID); ok {
		out.Model = le.Model
	}
	for _, child := range n.Children {
		out.Children = append(out.Children, s.apiTreeNode("project", projectKey, child, treeNodeCanActLive(child) && s.isLive(child.ID)))
	}
	return out
}

func hubRefFromTreeNodeID(id string) hubapi.Ref {
	if ref, err := hubapi.ParseRef(id); err == nil {
		return ref
	}
	return hubapi.LocalRef(id)
}

func (s *WebServer) liveEntry(sessionID string) (LiveEntry, bool) {
	if s.cfg.Roster == nil {
		return LiveEntry{}, false
	}
	return s.cfg.Roster.Find(sessionID)
}

func (s *WebServer) handleAPISession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	refText, sub, _ := strings.Cut(path, "/")
	if unescaped, err := url.PathUnescape(refText); err == nil {
		refText = unescaped
	}
	ref, err := hubapi.ParseRef(refText)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not found")
		return
	}
	routeID := ref.SessionID
	if ref.HostID != "local" {
		routeID = ref.String()
	}

	switch sub {
	case "":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		detail, ok := s.apiSessionDetail(routeID)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "session not found")
			return
		}
		writeAPIJSON(w, http.StatusOK, detail)
	case "details":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		detail, ok := s.apiSessionDetail(routeID)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "session not found")
			return
		}
		writeAPIJSON(w, http.StatusOK, detail)
	case "send":
		s.handleSend(w, r, routeID)
	case "tasks":
		s.renderSessionTasks(w, r, routeID)
	case "fork":
		s.handleAPIFork(w, r, routeID)
	case "clear":
		s.handleAPIClear(w, r, routeID)
	case "model":
		s.handleAPIModel(w, r, routeID)
	case "interrupt", "compact", "shutdown":
		s.handleSessionAction(w, r, routeID, sub)
	default:
		writeAPIError(w, http.StatusNotFound, "session not found")
	}
}

func (s *WebServer) apiSessionDetail(id string) (hubapi.SessionDetail, bool) {
	wd := s.workspaceData(id)
	if wd.ID == "" {
		return hubapi.SessionDetail{}, false
	}
	ref, err := hubapi.ParseRef(appRefFromRouteID(id))
	if err != nil {
		ref = hubapi.LocalRef(id)
	}
	live := s.isLive(id)
	detail := hubapi.SessionDetail{
		Ref:            ref.String(),
		HostID:         ref.HostID,
		SessionID:      ref.SessionID,
		Title:          wd.Title,
		State:          wd.State,
		Live:           live,
		Project:        filepath.Base(wd.WorkingDir),
		WorkingDir:     wd.WorkingDir,
		Branch:         wd.Branch,
		Model:          wd.Model,
		TurnCount:      wd.TurnCount,
		ForkLabel:      wd.ForkLabel,
		DivergenceTurn: wd.DivergenceTurn,
		Capabilities:   s.apiSessionCapabilities(id, live),
	}
	if detail.Project == "" || detail.Project == "." {
		detail.Project = "(no project)"
	}
	if live {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		appRef := appRefFromRouteID(id)
		if source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, appRef, ""); err == nil {
			if resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: appRef, IncludeTurns: true, ItemsView: "full"}); err == nil {
				appDetail := hubDetailFromAppThread(resp.Thread)
				if isLocalRouteID(id) && detail.TurnCount > 0 {
					appDetail.TurnCount = detail.TurnCount
				}
				appDetail.ParentSessionID = detail.ParentSessionID
				appDetail.DivergenceTurn = detail.DivergenceTurn
				appDetail.ForkLabel = detail.ForkLabel
				appDetail.IsSubagent = detail.IsSubagent
				detail = appDetail
			}
		}
	}
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			if detail.Title == "" {
				detail.Title = pastTitle(pe)
			}
			if detail.Model == "" {
				detail.Model = pe.Meta.Model
			}
			if detail.Profile == "" {
				detail.Profile = pe.Meta.ProfileID
			}
			if detail.TurnCount == 0 {
				detail.TurnCount = pe.Meta.TurnCount
			}
			detail.ParentSessionID = pe.Meta.ParentSessionID
			detail.DivergenceTurn = pe.Meta.DivergenceTurn
			detail.ForkLabel = pe.Meta.ForkLabel
			detail.IsSubagent = pe.Meta.IsSubagent
		}
	}
	return detail, true
}

func (s *WebServer) apiSessionCapabilities(id string, live bool) hubapi.SessionCapabilities {
	pastExists := false
	if s.cfg.Past != nil {
		_, pastExists = s.cfg.Past.Find(id)
	}
	caps := hubapi.SessionCapabilities{
		Fork:   pastExists,
		Resume: pastExists,
	}
	if !live && s.cfg.Spawner != nil && pastExists {
		caps.Send = true
	}
	if !caps.Send && !caps.Steer && !caps.Interrupt && !caps.Compact && !caps.Clear && !caps.Fork && !caps.Resume && !caps.Shutdown && !caps.ChangeModel {
		if live {
			caps.ReadOnlyReason = "live session source is unavailable"
		} else {
			caps.ReadOnlyReason = "session is not live and cannot be resumed"
		}
	}
	return caps
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

func (s *WebServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		http.NotFound(w, r)
		return
	}
	section := strings.TrimPrefix(r.URL.Path, "/settings")
	section = strings.TrimPrefix(section, "/")
	if section == "" {
		section = "general"
	}
	// Redirect the legacy /settings/launch URL to the serf-specific tab.
	if section == "launch" {
		http.Redirect(w, r, "/settings/launch-serf", http.StatusFound)
		return
	}
	partialURL := "/_partials/settings/" + section
	// For the per-project settings page, forward the cwd query param.
	if section == "project" {
		if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
			partialURL += "?cwd=" + url.QueryEscape(cwd)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": partialURL}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) renderSettingsPartial(w http.ResponseWriter, r *http.Request, section string) {
	// Redirect the legacy "launch" section to the serf-specific tab.
	if section == "launch" {
		http.Redirect(w, r, "/_partials/settings/launch-serf", http.StatusFound)
		return
	}
	// Project settings is its own workspace page, not nested in the global
	// settings shell.
	if section == "project" {
		s.renderProjectSettingsPartial(w, r)
		return
	}
	settingsTmpl, ok := s.settingsTmpls[section]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Group launch harness models by provider for the providers page.
	launchModelList, launchModelErr := serfLaunchModelList(r.Context(), s.cfg, "")
	if launchModelErr != nil {
		launchModelList = appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{launchModelListErrorDiagnostic(launchModelErr)},
		}
	}
	var providers []providerDisplay
	byProvider := map[string]int{} // provider name -> index in providers
	for _, m := range launchModelList.Data {
		if idx, exists := byProvider[m.Provider]; exists {
			providers[idx].Models = append(providers[idx].Models, m.Model)
		} else {
			byProvider[m.Provider] = len(providers)
			providers = append(providers, providerDisplay{Name: m.Provider, Models: []string{m.Model}})
		}
	}

	// Built-in agents are compiled into the binary (defaultPersona.txt etc.)
	// and don't have an on-disk file to open. EditPath stays empty so the
	// template can omit the link rather than rendering a broken one.
	agentNames := []string{"default", "explorer", "subagent"}
	agents := make([]agentDisplay, 0, len(agentNames))
	for _, name := range agentNames {
		agents = append(agents, agentDisplay{Name: name})
	}

	var pastCount int
	if s.cfg.Past != nil {
		pastCount = len(s.cfg.Past.AllMetas())
	}

	plugins, pluginsErr := s.discoverPluginsForSettings()
	skills := skillsFromPlugins(plugins)
	mcpPath := s.mcpConfigPathForSettings()
	mcps, mcpsErr := s.discoverMCPsForSettings(mcpPath)

	// Resolve canonical project cwd for the per-project settings page.
	var projectCWD string
	if section == "project" {
		if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
			if abs, err := filepath.Abs(cwd); err == nil {
				projectCWD = abs
			} else {
				projectCWD = cwd
			}
		}
	}

	// Build a deduplicated list of known projects for the project picker.
	var availableProjects []projectListItem
	if section == "project" && projectCWD == "" && s.cfg.Past != nil {
		seen := map[string]bool{}
		for _, meta := range s.cfg.Past.AllMetas() {
			cwd := meta.EnvInfo.WorkingDir
			if cwd == "" || seen[cwd] {
				continue
			}
			seen[cwd] = true
			availableProjects = append(availableProjects, projectListItem{
				CWD:  cwd,
				Name: filepath.Base(cwd),
			})
		}
		sort.Slice(availableProjects, func(i, j int) bool {
			return availableProjects[i].Name < availableProjects[j].Name
		})
	}

	// Compute display-only fields for the general/storage settings pages.
	pastIndexPath := tildeHome(s.cfg.PastIndexPath)
	pastIndexSize := fileSizeHuman(s.cfg.PastIndexPath)
	bearerTokenAge := ""
	if s.cfg.HubStateRoot != "" {
		bearerTokenAge = fileAgeHuman(filepath.Join(s.cfg.HubStateRoot, authTokenFile))
	}

	data := settingsData{
		Active:            section,
		HubAddr:           s.cfg.HubAddr,
		RunDir:            s.cfg.RunDir,
		StateDir:          s.cfg.StateDir,
		SpawnTimeout:      "30s",
		PastPerPage:       s.cfg.PastPerPage,
		PastIndexPath:     pastIndexPath,
		PastIndexSize:     pastIndexSize,
		BearerTokenAge:    bearerTokenAge,
		HubVersion:        Version,
		HubCommit:         buildinfo.GitSHA,
		Providers:         providers,
		ModelDiagnostics:  launchModelList.Diagnostics,
		Agents:            agents,
		Plugins:           plugins,
		PluginsError:      errString(pluginsErr),
		Skills:            skills,
		Mcps:              mcps,
		McpsError:         errString(mcpsErr),
		McpConfigPath:     mcpPath,
		PastCount:         pastCount,
		CodexLaunches:     s.cfg.CodexLaunches,
		ProjectCWD:        projectCWD,
		AvailableProjects: availableProjects,
	}

	// Render just the inner settings-content partial when htmx is targeting
	// the inner pane (rail click). Otherwise render the full shell so both
	// rail and content are visible (initial navigation into settings).
	tmplName := "settings"
	if r.Header.Get("HX-Target") == "settings-content" {
		tmplName = "settings-content"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := settingsTmpl.ExecuteTemplate(w, tmplName, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderProjectSettingsPartial serves the per-project settings page as its
// own workspace partial. It is not wrapped in the global settings shell —
// project settings get their own header (with the project's cwd) and pane.
func (s *WebServer) renderProjectSettingsPartial(w http.ResponseWriter, r *http.Request) {
	var projectCWD string
	if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			projectCWD = abs
		} else {
			projectCWD = cwd
		}
	}

	var availableProjects []projectListItem
	if projectCWD == "" && s.cfg.Past != nil {
		seen := map[string]bool{}
		for _, meta := range s.cfg.Past.AllMetas() {
			cwd := meta.EnvInfo.WorkingDir
			if cwd == "" || seen[cwd] {
				continue
			}
			seen[cwd] = true
			availableProjects = append(availableProjects, projectListItem{
				CWD:  cwd,
				Name: filepath.Base(cwd),
			})
		}
		sort.Slice(availableProjects, func(i, j int) bool {
			return availableProjects[i].Name < availableProjects[j].Name
		})
	}

	data := settingsData{
		Active:            "project",
		ProjectCWD:        projectCWD,
		AvailableProjects: availableProjects,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.projectSettingsTmpl.ExecuteTemplate(w, "project_settings", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// errString returns err.Error() or "" when err is nil. Used to thread
// recoverable settings-discovery errors into the template without a 5xx.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// tildeHome replaces the user's home directory prefix in path with "~".
// Returns path unchanged if home is empty or path does not start with home.
func tildeHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	if path == home {
		return "~"
	}
	return path
}

// fileAgeHuman returns a short human-readable description of how long ago the
// file at path was last modified (e.g. "created 3d ago"). Returns "" on error.
func fileAgeHuman(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	d := time.Since(info.ModTime())
	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < 2*time.Hour:
		return fmt.Sprintf("created %dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("created %dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("created %dd ago", int(d.Hours()/24))
	}
}

// fileSizeHuman returns a short human-readable file size string for path
// (e.g. "48 MB"). Returns "" if the file does not exist or stat fails.
func fileSizeHuman(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sz := info.Size()
	switch {
	case sz < 1<<10:
		return fmt.Sprintf("%d B", sz)
	case sz < 1<<20:
		return fmt.Sprintf("%d KB", sz>>10)
	case sz < 1<<30:
		return fmt.Sprintf("%d MB", sz>>20)
	default:
		return fmt.Sprintf("%d GB", sz>>30)
	}
}

// defaultPluginsRoot is the conventional XDG location for serf plugins:
// ~/.config/serf/plugins (or $XDG_CONFIG_HOME/serf/plugins).
func defaultPluginsRoot() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins")
}

// defaultMCPConfigPath is the conventional XDG location for the global
// MCP config (~/.config/serf/mcp.json), matching agent.globalMCPConfigPath.
func defaultMCPConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "mcp.json")
}

// pluginsRootForSettings returns explicit PluginDirs verbatim, or expands
// the default plugins root into one entry per immediate subdirectory
// containing a .claude-plugin/plugin.json manifest.
func (s *WebServer) pluginsRootForSettings() []string {
	if len(s.cfg.PluginDirs) > 0 {
		return s.cfg.PluginDirs
	}
	root := defaultPluginsRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err != nil {
			continue
		}
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// discoverPluginsForSettings loads plugin manifests for the Settings →
// Plugins pane. A discovery failure returns nil rows plus a non-nil error
// that the template renders inline.
func (s *WebServer) discoverPluginsForSettings() ([]pluginDisplay, error) {
	dirs := s.pluginsRootForSettings()
	if len(dirs) == 0 {
		return nil, nil
	}
	loaded, err := agent.LoadPlugins(dirs)
	if err != nil {
		return nil, err
	}
	out := make([]pluginDisplay, 0, len(loaded))
	for _, lp := range loaded {
		out = append(out, pluginDisplay{
			Name:    lp.Manifest.Name,
			Path:    lp.Dir,
			Version: lp.Manifest.Version,
			Counts: pluginCounts{
				Skills: len(lp.Skills),
				Agents: len(lp.Agents),
				Mcps:   len(lp.MCPConfigs),
				Hooks:  countHooks(lp.Hooks),
			},
			EditPath: editorurl.EditorURL(filepath.Join(lp.Dir, ".claude-plugin", "plugin.json")),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// countHooks sums all RegisteredHook entries across hook events.
func countHooks(h map[agent.HookEvent][]agent.RegisteredHook) int {
	n := 0
	for _, hs := range h {
		n += len(hs)
	}
	return n
}

// skillsFromPlugins flattens per-plugin skills directories into rows for
// the Skills pane. Plugins is the already-loaded list so we know each
// plugin's path and name.
func skillsFromPlugins(plugins []pluginDisplay) []skillDisplay {
	var out []skillDisplay
	for _, p := range plugins {
		out = append(out, collectSkillsForPlugin(p)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// collectSkillsForPlugin scans <pluginPath>/skills/ for SKILL.md files and
// returns one display row per skill. Returns nil if the dir is missing.
func collectSkillsForPlugin(p pluginDisplay) []skillDisplay {
	skillsDir := filepath.Join(p.Path, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	var out []skillDisplay
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		name, desc, ok := readSkillFrontmatter(skillFile)
		if !ok {
			continue
		}
		out = append(out, skillDisplay{
			Name:        name,
			Plugin:      p.Name,
			Description: desc,
			EditPath:    editorurl.EditorURL(skillFile),
		})
	}
	return out
}

// readSkillFrontmatter reads a SKILL.md file and returns its name and
// description from the YAML frontmatter. Returns ("","",false) if the
// file is missing, unparseable, or lacks required fields.
func readSkillFrontmatter(path string) (string, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	doc, err := frontmatter.Parse(string(data))
	if err != nil || doc.Meta == nil {
		return "", "", false
	}
	name, _ := doc.Meta["name"].(string)
	desc, _ := doc.Meta["description"].(string)
	if strings.TrimSpace(name) == "" || strings.TrimSpace(desc) == "" {
		return "", "", false
	}
	return name, desc, true
}

// mcpConfigPathForSettings returns the configured MCP file path, or the
// XDG default when WebConfig.MCPConfigPath is empty.
func (s *WebServer) mcpConfigPathForSettings() string {
	if s.cfg.MCPConfigPath != "" {
		return s.cfg.MCPConfigPath
	}
	return defaultMCPConfigPath()
}

// discoverMCPsForSettings reads the MCP config file at path and returns
// rows for the MCP servers pane. A missing file is the empty state, not
// an error. Parse errors return an inline error string.
func (s *WebServer) discoverMCPsForSettings(path string) ([]mcpDisplay, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	configs, err := agent.LoadMCPConfigFile(path)
	if err != nil {
		return nil, err
	}
	out := make([]mcpDisplay, 0, len(configs))
	for _, c := range configs {
		cmd := c.Command
		if cmd == "" {
			cmd = c.URL
		}
		out = append(out, mcpDisplay{
			Name:     c.Name,
			Command:  cmd,
			Args:     c.Args,
			Status:   mcpstatus.ProbeMCPStatus(c),
			Tools:    0,
			Agents:   nil,
			EditPath: editorurl.EditorURL(path),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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

// findNewSession polls the roster up to 3s for a daemon with the given pid.
// Returns the resolved session_id when found, or empty string on timeout.
func (s *WebServer) findNewSession(pid int) string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.cfg.Roster != nil {
			s.cfg.Roster.Refresh()
			for _, le := range s.cfg.Roster.List() {
				if le.PID == pid && le.SessionID != "" {
					return le.SessionID
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// handleWorkspaceSpawn renders the prompt-first spawn surface partial.
// Accepts an optional ?dir=<absolute path> query param. When present and the
// path is absolute and exists, it pre-fills the working_dir chip — used by
// the sidebar's per-project "+" button to open spawn already scoped to a
// project.
func (s *WebServer) handleWorkspaceSpawn(w http.ResponseWriter, r *http.Request) {
	defaultWorkingDir := "(pick a directory)"
	defaultWorkingDirValue := ""
	if dir := strings.TrimSpace(r.URL.Query().Get("dir")); dir != "" {
		if resolved, err := canonicalizeDir(dir); err == nil {
			defaultWorkingDir = resolved
			defaultWorkingDirValue = resolved
		}
	}
	data := spawnViewData{
		DefaultModel:           "(pick a model)",
		DefaultHarness:         "serf",
		DefaultWorkingDir:      defaultWorkingDir,
		DefaultWorkingDirValue: defaultWorkingDirValue,
		DefaultBranch:          "(default)",
		DefaultAccessMode:      "full",
		DefaultPrompt:          r.URL.Query().Get("prompt"),
		SafeEnv:                safeSpawnEnv(),
	}
	for _, descriptor := range launchHarnessDescriptors(s.cfg) {
		data.Harnesses = append(data.Harnesses, launchHarness{ID: descriptor.ID, Label: descriptor.Label})
	}
	if s.cfg.Past != nil {
		results := s.cfg.Past.Search("", 5, 0)
		for _, e := range results {
			if e.Meta.OriginalPrompt != "" {
				data.RecentPrompts = append(data.RecentPrompts, e.Meta.OriginalPrompt)
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.spawnTmpl.ExecuteTemplate(w, "spawn", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func safeSpawnEnv() map[string]string {
	out := map[string]string{}
	for _, name := range []string{"SERF_MODEL", "SERF_REASONING_EFFORT"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			out[name] = value
		}
	}
	return out
}

func launchHarnessIDs(cfg WebConfig) []string {
	descriptors := launchHarnessDescriptors(cfg)
	out := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		out = append(out, descriptor.ID)
	}
	return out
}

// handleApiSpawn spawns a new daemon and optionally sends the initial prompt.
func (s *WebServer) handleApiSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sendMaxRequestBytes)
	var req spawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateAppWireInputItems(req.Items); err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	for i, it := range req.Items {
		if len(it.Data) > sendMaxImageBytes {
			http.Error(w, fmt.Sprintf("items[%d] %q exceeds %d-byte limit", i, it.Name, sendMaxImageBytes), http.StatusRequestEntityTooLarge)
			return
		}
	}
	if s.cfg.Spawner == nil && len(s.cfg.CodexSources) == 0 && len(s.cfg.CodexLaunches) == 0 {
		writeSpawnError(w, appwire.Unavailable("spawner not configured"))
		return
	}
	resp, err := hubThreadStart(r.Context(), s.cfg, s.sources, appwire.ThreadStartParams{
		Harness:         req.Harness,
		CWD:             req.WorkingDir,
		Input:           append(inputItemsForText(req.Prompt), req.Items...),
		Model:           req.Model,
		Profile:         req.Agent,
		ReasoningEffort: req.ReasoningEffort,
		NonInteractive:  req.NonInteractive,
		LaunchOverrides: req.LaunchOverrides,
	})
	if err != nil {
		writeSpawnError(w, err)
		return
	}
	ref := hubRefFromAppThread(resp.Thread)
	writeAPIJSON(w, http.StatusOK, hubapi.SpawnResponse{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
	})
}

func writeSpawnError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if wire, ok := err.(appwire.WireError); ok {
		switch wire.Code {
		case appwire.CodeInvalidParams:
			status = http.StatusBadRequest
		case appwire.CodeUnavailable:
			status = http.StatusServiceUnavailable
		}
	}
	writeAPIWireError(w, status, err)
}

// handleApiModels returns the models the hub can spawn for. Hub-owned Serf
// launches report their model choices through the Serf launch harness contract;
// the direct live provider query remains a fallback for tests and non-spawning
// server configurations. Pricing and context-window metadata come from the
// embedded catalog where provider APIs don't carry it.
func (s *WebServer) handleApiModels(w http.ResponseWriter, r *http.Request) {
	harness := strings.TrimSpace(r.URL.Query().Get("harness"))
	workingDir := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if harness != "" && harness != "serf" && harness != "local" {
		resp, err := hubModelList(r.Context(), s.cfg, s.sources, appwire.ModelListParams{Harness: harness, CWD: workingDir})
		if err != nil {
			writeAPIWireError(w, http.StatusBadGateway, err)
			return
		}
		writeAPIJSON(w, http.StatusOK, modelDescriptorsToAPIModels(resp.Data))
		return
	}

	launchResp, err := serfLaunchModelList(r.Context(), s.cfg, workingDir)
	if err != nil && hasSerfLaunchModelLister(s.cfg) {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	models := modelDescriptorsToAPIModels(launchResp.Data)
	if len(models) == 0 && !hasSerfLaunchModelLister(s.cfg) {
		models = s.fetchLiveModels(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models) //nolint:errcheck
}

func serfLaunchModelsOrEmpty(ctx context.Context, cfg WebConfig) []appwire.ModelDescriptor {
	return serfLaunchModelListOrEmpty(ctx, cfg).Data
}

func serfLaunchModelListOrEmpty(ctx context.Context, cfg WebConfig) appwire.ModelListResponse {
	resp, err := serfLaunchModelList(ctx, cfg, "")
	if err != nil {
		return appwire.ModelListResponse{}
	}
	return resp
}

func launchModelListErrorDiagnostic(err error) appwire.ModelListDiagnostic {
	info := diagnostic.FromFields(string(diagnostic.SourceHub), "Model list unavailable", "", err.Error())
	return appwire.ModelListDiagnostic{
		Source:  string(info.Source),
		Title:   info.Title,
		Message: err.Error(),
		Hint:    info.Hint,
	}
}

func modelDescriptorsToAPIModels(models []appwire.ModelDescriptor) []map[string]any {
	if len(models) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(models))
	cat := llm.EmbeddedModelCatalog()
	for _, m := range models {
		if m.Provider == "" || m.Model == "" {
			continue
		}
		entry := map[string]any{
			"provider": m.Provider,
			"model":    m.Model,
		}
		if cat != nil {
			if mi := cat.GetModelInfo(m.Model); mi != nil {
				entry["display_name"] = mi.DisplayName
				entry["context_window"] = mi.ContextWindow
				entry["supports_tools"] = mi.SupportsTools
				entry["supports_reasoning"] = mi.SupportsReasoning
				entry["input_cost_per_million"] = mi.InputCostPerMillion
				entry["output_cost_per_million"] = mi.OutputCostPerMillion
			}
		}
		out = append(out, entry)
	}
	return out
}

func (s *WebServer) fetchLiveModels(ctx context.Context) []map[string]any {
	liveModelsCache.mu.Lock()
	if time.Now().Before(liveModelsCache.expires) && liveModelsCache.models != nil {
		out := liveModelsCache.models
		liveModelsCache.mu.Unlock()
		return out
	}
	liveModelsCache.mu.Unlock()

	c, _, _, err := cmdutil.LoadClient()
	if err != nil || c == nil {
		return nil
	}
	cat := llm.EmbeddedModelCatalog()

	var out []map[string]any
	for _, prov := range c.ProviderNames() {
		tag := c.BehaviorTagOf(prov)
		// Skip dual-route variants that surface the same models as their
		// primary route. openrouter-anthropic instances exist for specific
		// models whose tool-calling format requires the Anthropic-Messages
		// endpoint, but they list the same /models response as plain
		// openrouter. The daemon picks the correct route based on the model
		// name when spawning; the picker doesn't need to expose both.
		if tag == "openrouter-anthropic" {
			continue
		}
		listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		models, lerr := c.ListModels(listCtx, prov)
		cancel()
		if lerr != nil {
			// Provider doesn't support live listing or call failed —
			// skip this provider rather than fall back to a stale catalog.
			// User sees only providers we can confidently report on.
			continue
		}
		for _, m := range models {
			lower := strings.ToLower(m.ID)
			// Skip non-chat / non-completion models from the live list.
			if strings.Contains(lower, "embedding") ||
				strings.Contains(lower, "whisper") ||
				strings.Contains(lower, "tts") ||
				strings.Contains(lower, "dall-e") ||
				strings.Contains(lower, "moderation") ||
				strings.Contains(lower, "audio") ||
				strings.Contains(lower, "transcribe") ||
				strings.Contains(lower, "image") {
				continue
			}
			mi := catalogModelInfo(cat, m.ID)
			if tag == "openrouter" && (mi == nil || !mi.SupportsTools) {
				continue
			}
			// Use the registered provider name (prov), not m.Provider — wrapper
			// adapters like openrouter forward to openaicompat which reports
			// itself as "openai-compatible". The hub's spawn flow needs the
			// registered name (openrouter, openrouter-anthropic, etc.) so the
			// daemon spawns with the right adapter.
			entry := map[string]any{
				"provider":     prov,
				"model":        m.ID,
				"display_name": m.DisplayName,
			}
			if m.ContextWindow > 0 {
				entry["context_window"] = m.ContextWindow
			}
			if m.SupportsTools {
				entry["supports_tools"] = true
			}
			if m.SupportsReasoning {
				entry["supports_reasoning"] = true
			}
			// Keep catalog enrichment for static pricing/capability hints, but
			// do not replace live token limits with catalog values.
			if mi != nil {
				entry["input_cost_per_million"] = mi.InputCostPerMillion
				entry["output_cost_per_million"] = mi.OutputCostPerMillion
				if _, ok := entry["supports_tools"]; !ok {
					entry["supports_tools"] = mi.SupportsTools
				}
				if _, ok := entry["supports_reasoning"]; !ok {
					entry["supports_reasoning"] = mi.SupportsReasoning
				}
			}
			out = append(out, entry)
		}
	}

	liveModelsCache.mu.Lock()
	liveModelsCache.models = out
	liveModelsCache.expires = time.Now().Add(liveModelsTTL)
	liveModelsCache.mu.Unlock()
	return out
}

func catalogModelInfo(cat *llm.ModelCatalog, modelID string) *llm.ModelInfo {
	if cat == nil {
		return nil
	}
	return cat.GetModelInfo(modelID)
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

// handleSession is the router for public /s/<id>[/<sub>] routes.
// Session fragments live under /_partials/s/... so direct navigation always
// lands in the app shell instead of a standalone workspace fragment.
func (s *WebServer) handleSession(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/s/")
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
	case "":
		if r.Header.Get("HX-Request") == "true" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": "/_partials/s/" + id + "/workspace"}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "state":
		http.NotFound(w, r)
	case "meta":
		http.NotFound(w, r)
	case "details":
		http.NotFound(w, r)
	case "tasks":
		http.NotFound(w, r)
	case "send":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleSend(w, r, id)
	case "fork":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleFork(w, r, id)
	case "interrupt":
		s.handleSessionAction(w, r, id, "interrupt")
	case "compact":
		s.handleSessionAction(w, r, id, "compact")
	case "shutdown":
		s.handleSessionAction(w, r, id, "shutdown")
	case "clear":
		s.handleSessionAction(w, r, id, "clear")
	case "steer":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleSteer(w, r, id)
	case "queue":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleQueue(w, r, id)
	case "drain-as-steer":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleDrainAsSteer(w, r, id)
	default:
		// /s/<id>/images/<sha> — sha-addressed image fetch for replay.
		if strings.HasPrefix(sub, "images/") {
			sha := strings.TrimPrefix(sub, "images/")
			s.handleSessionImage(w, r, id, sha)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *WebServer) renderWorkspacePartial(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceData(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
	}
	if data.HomeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			data.HomeDir = home
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.workspaceTmpl.ExecuteTemplate(w, "workspace", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderDetailsPanel returns a side-panel with the session's verbose
// metadata: full session id, working dir, branch + sha, model, turn count,
// last input tokens, and (for forks) the parent session id and divergence
// turn. Triggered by clicking the "details" link in the workspace header.
func (s *WebServer) renderDetailsPanel(w http.ResponseWriter, r *http.Request, id string) {
	type detailsRow struct{ Label, Value string }
	var rows []detailsRow
	rows = append(rows, detailsRow{"source", sourceLabelFromRefText(appRefFromRouteID(id))})
	rows = append(rows, detailsRow{"session id", id})

	addMeta := func(m agent.SessionMeta) {
		if m.OriginalPrompt != "" {
			rows = append(rows, detailsRow{"prompt", m.OriginalPrompt})
		}
		if m.EnvInfo.WorkingDir != "" {
			rows = append(rows, detailsRow{"working dir", m.EnvInfo.WorkingDir})
		}
		if m.EnvInfo.GitBranch != "" {
			rows = append(rows, detailsRow{"branch", m.EnvInfo.GitBranch})
		}
		if m.Model != "" {
			rows = append(rows, detailsRow{"model", m.ProfileID + " · " + m.Model})
		}
		if m.TurnCount > 0 {
			rows = append(rows, detailsRow{"turns", fmt.Sprintf("%d", m.TurnCount)})
		}
		if m.LastInputTokens > 0 {
			rows = append(rows, detailsRow{"last input tokens", fmt.Sprintf("%d", m.LastInputTokens)})
		}
		if m.ParentSessionID != "" {
			rows = append(rows, detailsRow{"forked from", m.ParentSessionID})
			rows = append(rows, detailsRow{"divergence turn", fmt.Sprintf("%d", m.DivergenceTurn)})
		}
		if m.IsSubagent {
			rows = append(rows, detailsRow{"kind", "subagent"})
		}
	}

	if s.cfg.Roster != nil {
		if le, ok := s.cfg.Roster.Find(id); ok {
			rows = append(rows, detailsRow{"daemon", le.Address})
			rows = append(rows, detailsRow{"pid", fmt.Sprintf("%d", le.PID)})
		}
	}
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			addMeta(pe.Meta)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, `<header class="details-panel-header"><span>details</span><button class="details-panel-close" aria-label="close panel" onclick="document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))">✕</button></header>`)
	fmt.Fprintln(w, `<dl class="details-list">`)
	for _, row := range rows {
		fmt.Fprintf(w, `<dt>%s</dt><dd>%s</dd>`, htmlEscape(row.Label), htmlEscape(row.Value))
	}
	fmt.Fprintln(w, `</dl>`)
}

// renderSessionTasks returns the session's task list as JSON. For live
// sessions it proxies the daemon's GET /tasks; for ended sessions it reads
// the persisted <StateDir>/tasks/<id>.json. A missing file or absent
// session returns an empty array (200) so the UI doesn't have to special-
// case "no tasks yet".
func (s *WebServer) renderSessionTasks(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")

	ref := appRefFromRouteID(id)
	if source, err := sourceForThread(s.sources, ref, ""); err == nil {
		resp, err := source.ListTasks(r.Context(), appwire.TaskListParams{Ref: ref})
		if err == nil {
			_ = json.NewEncoder(w).Encode(resp.Data)
			return
		}
		// fall through to disk on daemon error
	}

	if isLocalRouteID(id) && s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok && pe.StateDir != "" {
			path := filepath.Join(pe.StateDir, "tasks", id+".json")
			if data, err := os.ReadFile(path); err == nil {
				_, _ = w.Write(data)
				return
			}
		}
	}

	_, _ = w.Write([]byte("[]\n"))
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// renderWorkspaceMeta returns the title-bar meta partial — status pill,
// branch, turn count. Polled every 2s by the workspace header so live
// state changes (idle → processing → ended) reflect promptly.
func (s *WebServer) renderWorkspaceMeta(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceData(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
	}
	if detail, ok := s.apiSessionDetail(id); ok {
		data.State = detail.State
		data.StateLabel = stateLabel(detail.State)
		data.TurnCount = detail.TurnCount
		if detail.Model != "" {
			data.Model = detail.Model
		}
		if detail.WorkingDir != "" {
			data.WorkingDir = detail.WorkingDir
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.workspaceTmpl.ExecuteTemplate(w, "workspace_meta", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) workspaceData(id string) WorkspaceData {
	if !isLocalRouteID(id) {
		ref := appRefFromRouteID(id)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, ref, "")
		if err != nil {
			return WorkspaceData{}
		}
		resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			return WorkspaceData{}
		}
		return workspaceDataFromAppThread(resp.Thread)
	}
	if s.cfg.Roster != nil {
		if le, ok := s.cfg.Roster.Find(id); ok {
			state := normalizeState(le.Status)
			data := WorkspaceData{
				ID:           id,
				SourceLabel:  "serf",
				Title:        liveTitle(id, le, s.cfg.Past),
				State:        state,
				StateLabel:   stateLabel(state),
				Model:        le.Model,
				WorkingDir:   le.WorkingDir,
				Capabilities: s.apiSessionCapabilities(id, true),
			}
			if status := s.fetchStatus(le); status != nil {
				if status.State != "" {
					data.State = normalizeState(status.State)
					data.StateLabel = stateLabel(data.State)
				}
				if status.Model != "" {
					data.Model = status.Model
				}
				if status.WorkingDir != "" {
					data.WorkingDir = status.WorkingDir
				}
				data.TurnCount = status.Turns
			}
			// Branch isn't on the rendezvous entry or daemon /status — fall
			// back to the past index where the agent persists EnvInfo.
			if s.cfg.Past != nil {
				if pe, ok := s.cfg.Past.Find(id); ok {
					data.Branch = pe.Meta.EnvInfo.GitBranch
					if data.WorkingDir == "" {
						data.WorkingDir = pe.Meta.EnvInfo.WorkingDir
					}
					s.fillForkLineage(&data, pe.Meta)
				}
			}
			if state == "ended" && s.cfg.Past != nil {
				if _, ok := s.cfg.Past.Find(id); ok {
					data.Capabilities = s.apiSessionCapabilities(id, false)
					return data
				}
			}
			data.Capabilities, data.ActiveTurnID = s.liveWorkspaceSnapshot(id, data.Capabilities)
			return data
		}
	}
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			data := WorkspaceData{
				ID:           id,
				SourceLabel:  "serf",
				Title:        pastTitle(pe),
				State:        "ended",
				StateLabel:   stateLabel("ended"),
				TurnCount:    pe.Meta.TurnCount,
				Model:        pe.Meta.Model,
				WorkingDir:   pe.Meta.EnvInfo.WorkingDir,
				Branch:       pe.Meta.EnvInfo.GitBranch,
				Capabilities: s.apiSessionCapabilities(id, false),
			}
			s.fillForkLineage(&data, pe.Meta)
			return data
		}
	}
	return WorkspaceData{}
}

func workspaceDataFromAppThread(thread appwire.Thread) WorkspaceData {
	ref := thread.Serf.Ref
	if ref == "" {
		ref = appwire.Ref{SourceID: thread.Source, ThreadID: thread.ID}.String()
	}
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	if title == "" {
		title = firstNonEmpty(thread.SessionID, thread.ID)
	}
	state := normalizeState(thread.Status.Type)
	if state == "" {
		state = "idle"
	}
	return WorkspaceData{
		ID:           ref,
		SourceLabel:  sourceLabelFromRefText(ref),
		Title:        title,
		State:        state,
		StateLabel:   stateLabel(state),
		TurnCount:    completedTurnCount(thread.Turns),
		ActiveTurnID: activeTurnIDFromAppwireThread(thread),
		RunningFor:   activeTurnRunningFor(thread),
		Model:        thread.ModelProvider,
		WorkingDir:   thread.CWD,
		Capabilities: hubCapabilitiesFromAppwire(thread.Serf.Capabilities),
	}
}

func activeTurnRunningFor(thread appwire.Thread) string {
	for _, turn := range thread.Turns {
		if turn.Status != appwire.TurnStatusInProgress || turn.StartedAt == nil || *turn.StartedAt <= 0 {
			continue
		}
		return compactDuration(time.Since(time.Unix(*turn.StartedAt, 0)))
	}
	return ""
}

func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		seconds := int(d.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%ds", seconds)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func activeTurnIDFromAppwireThread(thread appwire.Thread) string {
	for _, turn := range thread.Turns {
		if turn.Status == appwire.TurnStatusInProgress {
			return turn.ID
		}
	}
	return ""
}

// completedTurnCount counts only turns whose Status is "completed" — kata
// k5t4. Failed / canceled / in-flight turns don't count. Keeps the live
// status and the past-index display consistent.
func completedTurnCount(turns []appwire.Turn) int {
	n := 0
	for _, t := range turns {
		if t.Status == appwire.TurnStatusCompleted {
			n++
		}
	}
	return n
}

func (s *WebServer) liveWorkspaceCapabilities(id string, fallback hubapi.SessionCapabilities) hubapi.SessionCapabilities {
	caps, _ := s.liveWorkspaceSnapshot(id, fallback)
	return caps
}

func (s *WebServer) liveWorkspaceSnapshot(id string, fallback hubapi.SessionCapabilities) (hubapi.SessionCapabilities, string) {
	ref := appRefFromRouteID(id)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, ref, "")
	if err != nil {
		return fallback, ""
	}
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		return fallback, ""
	}
	caps := hubCapabilitiesFromAppwire(resp.Thread.Serf.Capabilities)
	caps.Resume = fallback.Resume
	return caps, activeTurnIDFromAppwireThread(resp.Thread)
}

func sourceLabelFromRefText(refText string) string {
	ref, err := appwire.ParseRef(refText)
	if err != nil {
		return "serf"
	}
	if ref.SourceID == "" || ref.SourceID == "local" {
		return "serf"
	}
	return ref.SourceID
}

// fillForkLineage populates the WorkspaceData fork-banner fields for the
// preserved-original side of a fork. The dim original is the meta with a
// non-empty ForkLabel; the new branch is the meta whose ParentSessionID
// equals this session's ID. ForkOfTitle is best-effort — if the new branch
// isn't in the past index, we leave it empty and the template falls back to
// "fork at turn N".
func (s *WebServer) fillForkLineage(data *WorkspaceData, m agent.SessionMeta) {
	if m.ForkLabel == "" {
		return
	}
	data.ForkLabel = m.ForkLabel
	data.DivergenceTurn = m.DivergenceTurn
	if s.cfg.Past == nil {
		return
	}
	for _, candidate := range s.cfg.Past.AllMetas() {
		if candidate.ParentSessionID == m.ID && !candidate.IsSubagent && candidate.ForkLabel == "" {
			data.ForkOfTitle = agent.SessionDisplayName(candidate)
			if data.ForkOfTitle == "" {
				data.ForkOfTitle = shortID(candidate.ID)
			}
			return
		}
	}
}

// liveTitle prefers a friendly title for a running session: pull
// OriginalPrompt from the past index if it's been written there, otherwise
// fall back to a short session ID.
func liveTitle(id string, le LiveEntry, past *PastIndex) string {
	if past != nil {
		if pe, ok := past.Find(id); ok {
			return pastTitle(pe)
		}
	}
	return shortID(id)
}

func pastTitle(pe PastEntry) string {
	if title := agent.SessionDisplayName(pe.Meta); title != "" {
		return title
	}
	return shortID(pe.Meta.ID)
}

func searchPastTitle(pe PastEntry) string {
	if title := strings.TrimSpace(pe.Meta.Name); title != "" {
		return title
	}
	return shortID(pe.Meta.ID)
}

func stateLabel(state string) string {
	switch state {
	case "awaiting":
		return "awaiting"
	case "active":
		return "active"
	case "warning":
		return "warning"
	case "idle":
		return "idle"
	case "notLoaded":
		return "notLoaded"
	}
	return state
}

func (s *WebServer) renderInputStrip(w http.ResponseWriter, r *http.Request, id string) {
	// Seed from workspaceData so WorkingDir/Branch are populated for both
	// live and past sessions, then refresh dynamic fields from /status when
	// the daemon is reachable.
	wd := s.workspaceData(id)
	data := map[string]any{
		"Model":          wd.Model,
		"WorkingDir":     wd.WorkingDir,
		"Branch":         wd.Branch,
		"ContextWindow":  0,
		"ContextPercent": 0,
		"ContextNumbers": "",
		"Cost":           wd.Cost,
		"State":          wd.State,
		"RunningFor":     wd.RunningFor,
	}
	if data["Model"] == "" {
		data["Model"] = "—"
	}
	if detail, ok := s.apiSessionDetail(id); ok {
		if detail.Model != "" {
			data["Model"] = detail.Model
		}
		data["State"] = detail.State
		data["ContextPercent"] = int(detail.ContextPressure * 100)
		data["ContextWindow"] = detail.ContextWindow
		data["ContextNumbers"] = formatContextNumbers(detail.ContextUsed, detail.ContextWindow, detail.ContextRemaining)
		if detail.WorkingDir != "" {
			data["WorkingDir"] = detail.WorkingDir
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.inputStripTmpl.ExecuteTemplate(w, "input_status", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func formatContextNumbers(used, window, remaining int) string {
	if window <= 0 {
		return ""
	}
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("%s / %s tokens (%s left)", formatTokenCount(used), formatTokenCount(window), formatTokenCount(remaining))
}

func formatTokenCount(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}

func (s *WebServer) handleSend(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, sendMaxRequestBytes)
	var body sendRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Text == "" && len(body.Items) == 0 {
		http.Error(w, "text or items required", http.StatusBadRequest)
		return
	}
	if err := validateAppWireInputItems(body.Items); err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	for i, it := range body.Items {
		if len(it.Data) > sendMaxImageBytes {
			http.Error(w, fmt.Sprintf("items[%d] %q exceeds %d-byte limit", i, it.Name, sendMaxImageBytes), http.StatusRequestEntityTooLarge)
			return
		}
	}
	ref := appRefFromRouteID(id)
	if !isLocalRouteID(id) {
		if _, managed := managedLaunchSourceIDForRef(s.cfg, ref); managed {
			source, err := sourceForThreadWithManagedLaunch(r.Context(), s.cfg, s.sources, ref, "")
			if err != nil {
				writeSessionActionError(w, r, err)
				return
			}
			if err := ensureThreadActionAvailable(r.Context(), source, ref, "", "send"); err != nil {
				writeSessionActionError(w, r, err)
				return
			}
		} else {
			if err := s.ensureSessionActionAvailable(id, "send"); err != nil {
				writeSessionActionError(w, r, err)
				return
			}
		}
	}

	resolve := func(forceResume bool) (LiveEntry, error) {
		if s.cfg.Roster == nil {
			return LiveEntry{}, fmt.Errorf("spawner not configured")
		}
		if !forceResume {
			if le, ok := s.cfg.Roster.Find(id); ok {
				return le, nil
			}
		}
		// Resume path: spawn the daemon and wait for it to register.
		if s.cfg.Spawner == nil {
			return LiveEntry{}, fmt.Errorf("spawner not configured")
		}
		lock := s.lockForSession(id)
		lock.Lock()
		defer lock.Unlock()
		if le, ok := s.cfg.Roster.Find(id); ok && !forceResume {
			return le, nil
		}
		resumeReq, err := s.resumeRequestFor(id)
		if err != nil {
			return LiveEntry{}, fmt.Errorf("resume: %w", err)
		}
		entry, err := s.cfg.Spawner.Resume(r.Context(), resumeReq)
		if err != nil {
			return LiveEntry{}, fmt.Errorf("resume: %w", err)
		}
		le := waitForRosterMatch(s.cfg.Roster, id, entry.PID, 5*time.Second)
		if le.Address == "" {
			return LiveEntry{}, fmt.Errorf("daemon not in roster after resume")
		}
		return le, nil
	}

	turnParams := appwire.TurnStartParams{Ref: ref, Input: inputItemsForText(body.Text)}
	turnParams.Input = append(turnParams.Input, body.Items...)
	startTurn := func(forceResume bool) error {
		if forceResume {
			if !isLocalRouteID(id) {
				return fmt.Errorf("remote source session is not resumable by local spawner")
			}
			if _, rerr := resolve(forceResume); rerr != nil {
				return rerr
			}
		} else if _, err := sourceForThread(s.sources, ref, ""); err != nil {
			if !isLocalRouteID(id) {
				return err
			}
			if _, rerr := resolve(forceResume); rerr != nil {
				return rerr
			}
		}
		source, err := sourceForThread(s.sources, ref, "")
		if err != nil {
			return err
		}
		if !forceResume {
			if err := ensureThreadActionAvailable(r.Context(), source, ref, "", "send"); err != nil {
				return err
			}
		}
		_, err = source.StartTurn(r.Context(), turnParams)
		return err
	}

	if err := startTurn(false); err != nil {
		if isActionUnavailable(err) {
			writeSessionActionError(w, r, err)
			return
		}
		if strings.Contains(err.Error(), "spawner not configured") {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if !shouldResumeAfterTurnStartError(err) {
			writeSessionActionError(w, r, err)
			return
		}
		if rerr := startTurn(true); rerr != nil {
			if strings.Contains(rerr.Error(), "spawner not configured") {
				http.Error(w, rerr.Error(), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "daemon unreachable: "+err.Error()+" (resume failed: "+rerr.Error()+")", http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
}

func (s *WebServer) resumeRequestFor(id string) (ResumeRequest, error) {
	return resumeRequestForConfig(s.cfg, id)
}

func inputItemsForText(text string) []appwire.InputItem {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []appwire.InputItem{{Type: "text", Text: text}}
}

// handleSteer forwards a steering message to the live daemon for the given
// session. Steer requires the session to already have a live daemon — we do
// not auto-resume on steer, since steering an ended session has no useful
// meaning (the model isn't running).
func (s *WebServer) handleSteer(w http.ResponseWriter, r *http.Request, id string) {
	var body steerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, "steer"); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := source.SteerTurn(r.Context(), appwire.TurnSteerParams{Ref: ref, ExpectedTurnID: strings.TrimSpace(body.TurnID), Input: inputItemsForText(body.Text)}); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleQueue forwards a turn/queue request to the live daemon for the given
// session. Unlike /send, queueing requires the session to be processing — the
// daemon returns Conflict when idle, which we surface as 409.
func (s *WebServer) handleQueue(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, sendMaxRequestBytes)
	var body queueRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" && len(body.Items) == 0 {
		http.Error(w, "text or items required", http.StatusBadRequest)
		return
	}
	if err := validateAppWireInputItems(body.Items); err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	for i, it := range body.Items {
		if len(it.Data) > sendMaxImageBytes {
			http.Error(w, fmt.Sprintf("items[%d] %q exceeds %d-byte limit", i, it.Name, sendMaxImageBytes), http.StatusRequestEntityTooLarge)
			return
		}
	}
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, "queue"); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := source.QueueTurn(r.Context(), appwire.TurnQueueParams{Ref: ref, Input: append(inputItemsForText(body.Text), body.Items...)}); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDrainAsSteer forwards a turn/drainAsSteer request (kata 0bq1 force-
// steer combined action). Drains the daemon's input queue into a single
// STEERING injection on the in-flight turn. Rides on the Steer capability;
// the daemon returns Conflict when idle or when the queue is empty.
func (s *WebServer) handleDrainAsSteer(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, sendMaxRequestBytes)
	var body drainAsSteerRequest
	// Empty bodies are valid (legacy classic drain). json.NewDecoder errors
	// only when the body has content that can't be parsed — silently
	// tolerate EOF / empty so the no-body path keeps working.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			// Only reject if the body wasn't empty. io.EOF from a zero
			// Content-Length-but-present body is normal; surface anything
			// else as a 400.
			if err.Error() != "EOF" {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	if err := validateAppWireInputItems(body.Items); err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	for i, it := range body.Items {
		if len(it.Data) > sendMaxImageBytes {
			http.Error(w, fmt.Sprintf("items[%d] %q exceeds %d-byte limit", i, it.Name, sendMaxImageBytes), http.StatusRequestEntityTooLarge)
			return
		}
	}
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, "steer"); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := source.DrainAsSteer(r.Context(), appwire.TurnDrainAsSteerParams{
		Ref:   ref,
		Input: append(inputItemsForText(strings.TrimSpace(body.Text)), body.Items...),
	}); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionAction forwards imperative actions to a daemon. Interrupt,
// clear, and shutdown remain live-only; compact can resume a known past
// session because it is a session-level maintenance action rather than an
// in-flight turn action.
func (s *WebServer) handleSessionAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ref := appRefFromRouteID(id)
	if action == "compact" {
		if !s.isLive(id) && !hubKnowsRef(s.cfg, ref) {
			http.NotFound(w, r)
			return
		}
		if err := compactThreadWithResume(r.Context(), s.cfg, s.sources, appwire.ThreadCompactStartParams{Ref: ref}); err != nil {
			writeSessionActionError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.isLive(id) {
		http.NotFound(w, r)
		return
	}
	if err := s.ensureSessionActionAvailable(id, action); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body sessionActionRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch action {
	case "interrupt":
		err = source.InterruptTurn(r.Context(), appwire.TurnInterruptParams{Ref: ref, ExpectedTurnID: strings.TrimSpace(body.TurnID)})
	case "clear":
		_, err = source.ClearThread(r.Context(), appwire.ThreadClearParams{Ref: ref})
	case "shutdown":
		err = source.ShutdownThread(r.Context(), appwire.ThreadShutdownParams{Ref: ref})
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *WebServer) ensureSessionActionAvailable(id, action string) error {
	detail, ok := s.apiSessionDetail(id)
	if !ok {
		return appwire.Unavailable("session action is not available")
	}
	if sessionCapabilityAvailable(detail.Capabilities, action) {
		return nil
	}
	return appwire.Unavailable(action + " is not available for this session")
}

func sessionCapabilityAvailable(caps hubapi.SessionCapabilities, action string) bool {
	switch action {
	case "send":
		return caps.Send
	case "steer":
		return caps.Steer
	case "interrupt":
		return caps.Interrupt
	case "compact":
		return caps.Compact
	case "clear":
		return caps.Clear
	case "fork":
		return caps.Fork
	case "shutdown":
		return caps.Shutdown
	case "model":
		return caps.ChangeModel
	case "queue":
		return caps.Queue
	default:
		return false
	}
}

func writeSessionActionError(w http.ResponseWriter, r *http.Request, err error) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	status := http.StatusBadGateway
	if wire, ok := wireErrorFromError(err); ok {
		status = statusForWireError(wire, status)
		if info := serfErrorInfoFromData(wire.Data); info != "" {
			w.Header().Set("X-Serf-Error-Info", info)
		}
	}
	http.Error(w, err.Error(), status)
}

func isActionUnavailable(err error) bool {
	wire, ok := wireErrorFromError(err)
	return ok && wire.Code == appwire.CodeUnavailable && serfErrorInfoFromData(wire.Data) == string(appwire.ErrorActionUnavailable)
}

func (s *WebServer) handleFork(w http.ResponseWriter, r *http.Request, parentID string) {
	var body forkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	childID, err := s.forkSession(parentID, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"child_session_id": childID}) //nolint:errcheck
}

func (s *WebServer) handleAPIFork(w http.ResponseWriter, r *http.Request, parentID string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body forkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	childID, err := s.forkSession(parentID, body)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ref := hubapi.LocalRef(childID)
	writeAPIJSON(w, http.StatusOK, hubapi.RefResponse{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
	})
}

func (s *WebServer) forkSession(parentID string, body forkRequest) (string, error) {
	// Resolve the state dir for the parent session. Forks must write into
	// the same project's state-dir as the parent (so they appear in the
	// project tree). Past index knows the per-project state-dir; cfg.StateDir
	// is the parent of all projects and would point ForkSession at the wrong
	// directory.
	var stateDir string
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(parentID); ok {
			stateDir = pe.StateDir
		}
	}
	if stateDir == "" {
		stateDir = s.cfg.StateDir
	}
	if stateDir == "" {
		return "", fmt.Errorf("state dir not resolvable for parent session")
	}

	childID, err := agent.ForkSession(stateDir, parentID, body.Turn, body.EditedMessage, body.Label)
	if err != nil {
		return "", err
	}
	// Refresh past index so the new session shows up immediately in the sidebar.
	if s.cfg.Past != nil {
		_ = s.cfg.Past.Rebuild()
	}
	return childID, nil
}

// waitForRosterMatch polls the roster until it sees a daemon with the given PID
// and session ID, or until timeout. Returns the matched LiveEntry (Address == "" on timeout).
func waitForRosterMatch(r *Roster, sessionID string, pid int, timeout time.Duration) LiveEntry {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.Refresh()
		if le, ok := r.Find(sessionID); ok && le.PID == pid {
			return le
		}
		time.Sleep(150 * time.Millisecond)
	}
	return LiveEntry{}
}

// fetchStatus reads /status from the daemon at le.Address, returning nil on any error.
func (s *WebServer) fetchStatus(le LiveEntry) *daemonStatus {
	client := &http.Client{Timeout: 1 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+le.Address+"/status", nil) //nolint:gosec
	if err != nil {
		return nil
	}
	setDaemonAuthorization(req.Header, le.HubToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var info daemonStatus
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil
	}
	return &info
}
