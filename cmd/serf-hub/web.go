package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/frontmatter"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/internal/hubapi"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

// WebConfig is everything the web server needs.
type WebConfig struct {
	HubAddr       string
	RunDir        string // run directory where rendezvous files live
	Roster        *Roster
	Past          *PastIndex
	Spawner       Spawner           // optional; nil disables spawn
	Models        []modelDescriptor // available models for the spawn chip
	PastPerPage   int               // results per page for /past; defaults to 50 when zero
	StateDir      string            // root of the projects/<sha> state directory; needed for ForkSession
	PluginDirs    []string          // explicit plugin dirs; when empty, default to ~/.config/serf/plugins/*
	MCPConfigPath string            // MCP config file path; when empty, default to ~/.config/serf/mcp.json
	CodexSources  []appsource.CodexSourceConfig
	CodexLaunches []CodexLaunchConfig
	CodexLauncher *CodexLauncher
}

// Spawner forks a serf serve subprocess and waits for its rendezvous file to appear.
// Returns the discovered Entry on success.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error)
	Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error)
}

// modelDescriptor is a provider/model pair used by the spawn chip and /api/models.
type modelDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// WebServer wires routes, templates, and middleware.
type WebServer struct {
	cfg            WebConfig
	appTmpl        *template.Template
	sidebarTmpl    *template.Template
	workspaceTmpl  *template.Template
	spawnTmpl      *template.Template
	inputStripTmpl *template.Template
	settingsTmpls  map[string]*template.Template
	sse            *SSEProxy
	appRPC         *appserver.Server
	sources        *appsource.Registry
	startedAt      time.Time

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
	settingsSections := []string{"general", "theme", "notifications", "providers", "agents", "plugins", "skills", "mcp", "hub", "storage"}
	settingsTmpls := make(map[string]*template.Template, len(settingsSections))
	for _, sec := range settingsSections {
		settingsTmpls[sec] = template.Must(template.ParseFS(templatesFS,
			"templates/partials/settings.html",
			"templates/partials/settings/"+sec+".html",
		))
	}
	var sse *SSEProxy
	if cfg.Roster != nil {
		sse = NewSSEProxy(cfg.Roster)
	}
	sources := newHubSourceRegistry(cfg)
	if cfg.CodexLauncher == nil && len(cfg.CodexLaunches) > 0 {
		cfg.CodexLauncher = NewCodexLauncher(cfg.CodexLaunches)
	}
	web := &WebServer{
		cfg: cfg, appTmpl: appTmpl, sidebarTmpl: sidebarTmpl,
		workspaceTmpl: workspaceTmpl, spawnTmpl: spawnTmpl, inputStripTmpl: inputStripTmpl,
		settingsTmpls: settingsTmpls,
		sse:           sse,
		sources:       sources,
		startedAt:     time.Now().UTC(),
		resumeLocks:   map[string]*sync.Mutex{},
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
	mux.HandleFunc("/past/", s.handlePastID) // /past/<id>/replay

	// Settings
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/settings/", s.handleSettings)

	// API
	mux.HandleFunc("/api/spawn", s.handleApiSpawn)
	mux.HandleFunc("/api/models", s.handleApiModels)
	mux.HandleFunc("/api/dirs", s.handleApiDirs)
	mux.HandleFunc("/api/search", s.handleApiSearch)
	mux.HandleFunc("/api/health", s.handleAPIHealth)
	mux.HandleFunc("/api/tree", s.handleAPITree)
	mux.HandleFunc("/api/spawn-schema", s.handleAPISpawnSchema)
	mux.HandleFunc("/api/sessions/", s.handleAPISession)

	guard := SameOriginGuard(s.cfg.HubAddr)
	return guard(CSPMiddleware(mux))
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

// searchResult is one item in the /api/search response.
type searchResult struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	State   string `json:"state"`
	Age     string `json:"age"`
}

// searchResponse is the JSON envelope returned by /api/search.
type searchResponse struct {
	Live []searchResult `json:"live"`
	Past []searchResult `json:"past"`
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
			title := e.Meta.OriginalPrompt
			if title == "" {
				title = shortID(e.Meta.ID)
			}
			resp.Past = append(resp.Past, searchResult{
				ID:      e.Meta.ID,
				Title:   title,
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
	var live []LiveEntry
	if s.cfg.Roster != nil {
		live = s.cfg.Roster.List()
	}
	var metas []agent.SessionMeta
	if s.cfg.Past != nil {
		metas = s.cfg.Past.AllMetas()
	}
	tree := BuildTree(metas, live)
	resp := hubapi.TreeResponse{
		GeneratedAt: time.Now().UTC(),
		Sources: []hubapi.Source{{
			ID:     "local",
			Label:  "this host",
			Kind:   "local",
			Online: true,
		}},
	}
	for _, n := range tree.Live {
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
			ap.Sessions = append(ap.Sessions, s.apiTreeNode("project", key, n, s.isLive(n.ID)))
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
		Ref:             ref.String(),
		HostID:          ref.HostID,
		SessionID:       ref.SessionID,
		Title:           title,
		State:           state,
		Live:            live,
		Project:         project,
		WorkingDir:      thread.CWD,
		Model:           thread.ModelProvider,
		Profile:         thread.Serf.Profile,
		TurnCount:       len(thread.Turns),
		ContextPressure: thread.Serf.ContextPressure,
		Capabilities:    hubCapabilitiesFromAppwire(thread.Serf.Capabilities),
	}
	if detail.SessionID == "" {
		detail.SessionID = thread.ID
	}
	detail.Streams.TranscriptFollow = "/api/sessions/" + ref.PathEscaped() + "/events?mode=transcript-follow"
	if detail.Live {
		detail.Streams.Live = "/api/sessions/" + ref.PathEscaped() + "/events?mode=live"
	}
	return detail
}

func (s *WebServer) isLive(sessionID string) bool {
	if !isLocalRouteID(sessionID) {
		_, err := sourceForThread(s.sources, appRefFromRouteID(sessionID), "")
		return err == nil
	}
	if s.cfg.Roster == nil {
		return false
	}
	_, ok := s.cfg.Roster.Find(sessionID)
	return ok
}

func (s *WebServer) apiTreeNode(scope, projectKey string, n TreeNode, live bool) hubapi.TreeNode {
	ref := hubapi.LocalRef(n.ID).String()
	rowID := scope + ":" + ref
	if projectKey != "" {
		rowID = scope + ":" + projectKey + ":" + ref
	}
	out := hubapi.TreeNode{
		RowID:     rowID,
		Ref:       ref,
		HostID:    "local",
		SessionID: n.ID,
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
		out.Children = append(out.Children, s.apiTreeNode("project", projectKey, child, s.isLive(child.ID)))
	}
	return out
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
	case "events":
		s.handleAPISessionEvents(w, r, routeID)
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
		Streams: hubapi.SessionStreams{
			TranscriptFollow: "/api/sessions/" + ref.PathEscaped() + "/events?mode=transcript-follow",
		},
	}
	if detail.Project == "" || detail.Project == "." {
		detail.Project = "(no project)"
	}
	if live {
		detail.Streams.Live = "/api/sessions/" + ref.PathEscaped() + "/events?mode=live"
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		appRef := appRefFromRouteID(id)
		if source, err := sourceForThread(s.sources, appRef, ""); err == nil {
			if resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: appRef, IncludeTurns: true, ItemsView: "full"}); err == nil {
				appDetail := hubDetailFromAppThread(resp.Thread)
				appDetail.Streams = detail.Streams
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
			detail.Streams.Replay = "/api/sessions/" + ref.PathEscaped() + "/events?mode=replay"
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

func (s *WebServer) handleAPISessionEvents(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "transcript-follow"
	}
	switch mode {
	case "live":
		s.serveAppwireEvents(w, r, id, false)
	case "transcript-follow":
		if s.isLive(id) {
			s.serveAppwireEvents(w, r, id, true)
			return
		}
		if s.cfg.Past != nil {
			if pe, ok := s.cfg.Past.Find(id); ok {
				s.serveReplay(w, r, pe)
				return
			}
		}
		writeAPIError(w, http.StatusNotFound, "session not found")
	case "replay":
		if s.cfg.Past != nil {
			if pe, ok := s.cfg.Past.Find(id); ok {
				s.serveReplay(w, r, pe)
				return
			}
		}
		writeAPIError(w, http.StatusNotFound, "session not found")
	default:
		writeAPIError(w, http.StatusBadRequest, "unknown events mode")
	}
}

func (s *WebServer) serveAppwireEvents(w http.ResponseWriter, r *http.Request, id string, includeReplay bool) {
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	ctx := r.Context()
	if includeReplay {
		if resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"}); err == nil {
			writeThreadReplaySSE(w, flusher, resp.Thread)
		}
	}
	notifications, err := source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
	if err != nil {
		writeSSE(w, flusher, "ERROR", map[string]any{"error": err.Error()})
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case notification, ok := <-notifications:
			if !ok {
				return
			}
			writeNotificationSSE(w, flusher, notification)
		}
	}
}

func writeThreadReplaySSE(w io.Writer, flusher http.Flusher, thread appwire.Thread) {
	writeSSE(w, flusher, "SESSION_START", map[string]any{
		"session_id": thread.SessionID,
		"model":      thread.ModelProvider,
		"profile":    thread.Serf.Profile,
	})
	for _, turn := range thread.Turns {
		turnIndex := turnIndexFromAppwireID(turn.ID)
		for _, item := range turn.Items {
			writeItemReplaySSE(w, flusher, turnIndex, item)
		}
		if turn.Status == appwire.TurnStatusFailed {
			message := "turn failed"
			if turn.Error != nil && strings.TrimSpace(turn.Error.Message) != "" {
				message = turn.Error.Message
			}
			writeSSE(w, flusher, "ERROR", errorPayload(message, turn.Error))
		}
	}
}

func writeItemReplaySSE(w io.Writer, flusher http.Flusher, turnIndex int, item appwire.ThreadItem) {
	switch item.Type {
	case "user_message":
		payload := map[string]any{"text": item.Text, "turn": turnIndex}
		var images []map[string]any
		for _, image := range item.Images {
			record := map[string]any{
				"media_type": image.MediaType,
				"name":       image.Name,
			}
			if image.Metadata != nil {
				record["sha"] = image.Metadata["sha"]
				if size := image.Metadata["size"]; size != "" {
					record["size"] = size
				}
			}
			images = append(images, record)
		}
		if len(images) > 0 {
			payload["images"] = images
		}
		writeSSE(w, flusher, "USER_INPUT", payload)
	case "steering":
		writeSSE(w, flusher, "STEERING_INJECTED", map[string]any{"text": item.Text})
	case "agent_message":
		if item.Text != "" {
			writeSSE(w, flusher, "ASSISTANT_TEXT_END", map[string]any{"text": item.Text})
		}
	case "tool_call":
		writeSSE(w, flusher, "TOOL_CALL_START", map[string]any{
			"call_id":        firstNonEmpty(item.CallID, item.ID),
			"tool_name":      item.ToolName,
			"arguments_json": item.ArgumentsJSON,
		})
		if item.Output != "" {
			writeSSE(w, flusher, "TOOL_CALL_OUTPUT_DELTA", map[string]any{
				"call_id": firstNonEmpty(item.CallID, item.ID),
				"delta":   item.Output,
			})
		}
		writeSSE(w, flusher, "TOOL_CALL_END", map[string]any{
			"call_id":        firstNonEmpty(item.CallID, item.ID),
			"item_id":        item.ID,
			"tool_name":      item.ToolName,
			"arguments_json": item.ArgumentsJSON,
			"output":         item.Output,
			"error":          item.Error,
			"tool_state":     item.Raw,
		})
	}
}

func writeNotificationSSE(w io.Writer, flusher http.Flusher, notification appwire.Notification) {
	switch notification.Method {
	case appwire.NotifyThreadStatusChanged:
		var params appwire.ThreadStatusChangedParams
		if json.Unmarshal(notification.Params, &params) == nil {
			writeSSE(w, flusher, "THREAD_STATUS_CHANGED", map[string]any{"status": params.Status.Type})
		}
	case appwire.NotifyItemStarted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			switch params.Item.Type {
			case "user_message":
				writeSSE(w, flusher, "USER_INPUT", map[string]any{"text": params.Item.Text, "turn": turnIndexFromAppwireID(params.Item.TurnID)})
			case "agent_message":
				writeSSE(w, flusher, "ASSISTANT_TEXT_START", map[string]any{})
			case "tool_call":
				writeSSE(w, flusher, "TOOL_CALL_START", map[string]any{
					"call_id":        firstNonEmpty(params.Item.CallID, params.Item.ID),
					"tool_name":      params.Item.ToolName,
					"arguments_json": params.Item.ArgumentsJSON,
				})
			}
		}
	case appwire.NotifyAgentMessageDelta:
		var params appwire.AgentMessageDeltaParams
		if json.Unmarshal(notification.Params, &params) == nil {
			writeSSE(w, flusher, "ASSISTANT_TEXT_DELTA", map[string]any{"delta": params.Delta})
		}
	case appwire.NotifyToolOutputDelta:
		var params struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			writeSSE(w, flusher, "TOOL_CALL_OUTPUT_DELTA", map[string]any{"call_id": params.ItemID, "delta": params.Delta})
		}
	case appwire.NotifyItemCompleted:
		var params struct {
			Item appwire.ThreadItem `json:"item"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			if params.Item.Type == "agent_message" {
				writeSSE(w, flusher, "ASSISTANT_TEXT_END", map[string]any{"text": params.Item.Text})
			}
			if params.Item.Type == "tool_call" {
				writeSSE(w, flusher, "TOOL_CALL_END", map[string]any{
					"call_id":        firstNonEmpty(params.Item.CallID, params.Item.ID),
					"item_id":        params.Item.ID,
					"tool_name":      params.Item.ToolName,
					"arguments_json": params.Item.ArgumentsJSON,
					"output":         params.Item.Output,
					"error":          params.Item.Error,
					"tool_state":     params.Item.Raw,
				})
			}
		}
	case appwire.NotifyTurnCompleted:
		var params struct {
			Turn appwire.Turn `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) == nil && params.Turn.Status == appwire.TurnStatusFailed {
			message := "turn failed"
			if params.Turn.Error != nil && strings.TrimSpace(params.Turn.Error.Message) != "" {
				message = params.Turn.Error.Message
			}
			writeSSE(w, flusher, "ERROR", errorPayload(message, params.Turn.Error))
		}
	case appwire.NotifyWarning:
		writeSSE(w, flusher, "WARNING", warningPayload(notification.Params))
	case appwire.NotifySerfSteeringInjected:
		var params struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(notification.Params, &params) == nil {
			writeSSE(w, flusher, "STEERING_INJECTED", map[string]any{"text": params.Text})
		}
	}
}

func errorPayload(message string, turnErr *appwire.TurnError) map[string]any {
	payload := map[string]any{"error": message}
	if turnErr != nil {
		if turnErr.Source != "" {
			payload["source"] = turnErr.Source
		}
		if turnErr.Title != "" {
			payload["title"] = turnErr.Title
		}
		if turnErr.Hint != "" {
			payload["hint"] = turnErr.Hint
		}
	}
	addDiagnosticDefaults(payload, message)
	return payload
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

func writeSSE(w io.Writer, flusher http.Flusher, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	if flusher != nil {
		flusher.Flush()
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func turnIndexFromAppwireID(raw string) int {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "turn_")
	var n int
	_, _ = fmt.Sscanf(raw, "%d", &n)
	return n
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

// agentDisplay is one row in Settings → Agents.
type agentDisplay struct {
	Name     string
	EditPath template.URL
}

// pluginCounts summarises components contributed by a plugin.
type pluginCounts struct {
	Skills int
	Agents int
	Mcps   int
	Hooks  int
}

// pluginDisplay is one row in Settings → Plugins.
type pluginDisplay struct {
	Name     string
	Path     string
	Version  string
	Counts   pluginCounts
	EditPath template.URL
}

// skillDisplay is one row in Settings → Skills.
type skillDisplay struct {
	Name        string
	Plugin      string
	Description string
	EditPath    template.URL
}

// mcpDisplay is one row in Settings → MCP servers.
type mcpDisplay struct {
	Name     string
	Command  string
	Args     []string
	Status   string // "running" | "stopped" | "error" | "unknown"
	Tools    int
	Agents   []string
	EditPath template.URL
}

// settingsData is the template data passed to all settings section templates.
type settingsData struct {
	Active        string
	HubAddr       string
	RunDir        string
	StateDir      string
	SpawnTimeout  string
	PastPerPage   int
	Providers     []providerDisplay
	Agents        []agentDisplay
	Plugins       []pluginDisplay
	PluginsError  string
	Skills        []skillDisplay
	Mcps          []mcpDisplay
	McpsError     string
	McpConfigPath string
	PastCount     int
}

// providerDisplay groups model descriptors by provider for the providers page.
type providerDisplay struct {
	Name   string
	Models []string
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.appTmpl.ExecuteTemplate(w, "app", map[string]string{"WorkspaceURL": "/_partials/settings/" + section}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) renderSettingsPartial(w http.ResponseWriter, r *http.Request, section string) {
	settingsTmpl, ok := s.settingsTmpls[section]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Group flat Models list by provider for the providers page.
	var providers []providerDisplay
	byProvider := map[string]int{} // provider name -> index in providers
	for _, m := range s.cfg.Models {
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

	data := settingsData{
		Active:        section,
		HubAddr:       s.cfg.HubAddr,
		RunDir:        s.cfg.RunDir,
		StateDir:      s.cfg.StateDir,
		SpawnTimeout:  "30s",
		PastPerPage:   s.cfg.PastPerPage,
		Providers:     providers,
		Agents:        agents,
		Plugins:       plugins,
		PluginsError:  errString(pluginsErr),
		Skills:        skills,
		Mcps:          mcps,
		McpsError:     errString(mcpsErr),
		McpConfigPath: mcpPath,
		PastCount:     pastCount,
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

// errString returns err.Error() or "" when err is nil. Used to thread
// recoverable settings-discovery errors into the template without a 5xx.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
			EditPath: editorURL(filepath.Join(lp.Dir, ".claude-plugin", "plugin.json")),
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
			EditPath:    editorURL(skillFile),
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
			Status:   probeMCPStatus(c),
			Tools:    0,
			Agents:   nil,
			EditPath: editorURL(path),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *WebServer) handleSidebar(w http.ResponseWriter, r *http.Request) {
	var metas []agent.SessionMeta
	if s.cfg.Past != nil {
		metas = s.cfg.Past.AllMetas()
	}
	var live []LiveEntry
	if s.cfg.Roster != nil {
		live = s.cfg.Roster.List()
	}
	tree := BuildTree(metas, live)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.sidebarTmpl.ExecuteTemplate(w, "sidebar", tree); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) handleWorkspaceEmpty(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<div class="workspace-empty"><p>No session selected.</p><p style="margin-top:1em"><a href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ new session</a></p></div>`)
}

// handlePastID dispatches /past/<id>/replay (the only past route still served
// by the new app — the workspace partial points its renderer at it for past
// sessions).
func (s *WebServer) handlePastID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/past/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" || s.cfg.Past == nil {
		http.NotFound(w, r)
		return
	}
	entry, ok := s.cfg.Past.Find(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	if tail != "replay" {
		http.NotFound(w, r)
		return
	}
	s.serveReplay(w, r, entry)
}

// serveReplay reads <state_dir>/sessions/<id>.transcript.jsonl and translates
// each turn into the SSE events the browser renderer expects. The transcript
// is a record of message-shaped Turns; the renderer is built around the
// daemon's runtime event stream, so we map between the two here.
func (s *WebServer) serveReplay(w http.ResponseWriter, r *http.Request, entry PastEntry) {
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	f, err := os.Open(transcriptPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	id := uint64(0)
	emit := func(event string, payload any) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		id++
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, data)
		flusher.Flush()
	}

	// call_id -> tool_name, so TOOL_RESULTS turns can name the tool the
	// renderer is closing out.
	toolNames := map[string]string{}

	first := true
	entryIndex := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		if first {
			first = false
			if head.Kind == "header" {
				var h replayHeader
				if err := json.Unmarshal(raw, &h); err == nil {
					emit("SESSION_START", map[string]any{
						"session_id": h.SessionID,
						"profile":    h.ProfileID,
						"model":      h.Model,
						"restored":   true,
					})
				}
				continue
			}
		}
		if head.Kind == "api_call" {
			var call agent.TranscriptAPICall
			if err := json.Unmarshal(raw, &call); err == nil && strings.TrimSpace(call.Error) != "" {
				emit("ERROR", errorPayload(call.Error, nil))
			}
			continue
		}
		if head.Kind != "entry" {
			continue
		}
		var entryRec replayEntry
		if err := json.Unmarshal(raw, &entryRec); err != nil {
			continue
		}
		entryIndex++
		emitTurnEvents(emit, entryIndex, entryRec.Turn, toolNames)
	}
	// Tell EventSource clients we're done. The browser would otherwise
	// auto-reconnect on close and the replay would re-run from the top.
	emit("REPLAY_DONE", map[string]any{})
}

type replayHeader struct {
	SessionID string `json:"session_id"`
	ProfileID string `json:"profile_id"`
	Model     string `json:"model"`
}

type replayEntry struct {
	Turn replayTurn `json:"turn"`
}

type replayTurn struct {
	Kind    string        `json:"kind"`
	Message replayMessage `json:"message"`
}

type replayMessage struct {
	Role    string       `json:"role"`
	Content []replayPart `json:"content"`
}

type replayPart struct {
	Kind       string            `json:"kind"`
	Text       string            `json:"text,omitempty"`
	Image      *replayImage      `json:"image,omitempty"`
	ToolCall   *replayToolCall   `json:"tool_call,omitempty"`
	ToolResult *replayToolResult `json:"tool_result,omitempty"`
}

type replayImage struct {
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Name      string `json:"name,omitempty"`
}

type replayToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type replayToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    any    `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// emitTurnEvents translates a single transcript Turn into the SSE events
// the renderer consumes. toolNames carries call_id -> tool_name across calls
// so we can populate TOOL_CALL_END from a TOOL_RESULTS turn.
func emitTurnEvents(emit func(string, any), turnIndex int, turn replayTurn, toolNames map[string]string) {
	switch turn.Kind {
	case "USER_INPUT":
		text := joinText(turn.Message.Content)
		payload := map[string]any{"text": text, "turn": turnIndex}
		var images []map[string]any
		for _, p := range turn.Message.Content {
			if p.Kind != "image" || p.Image == nil || len(p.Image.Data) == 0 {
				continue
			}
			// Strip raw bytes from the replay payload — multi-MB transcripts
			// were re-emitted on every reload. The renderer fetches lazily
			// via /s/<id>/images/<sha> instead.
			images = append(images, map[string]any{
				"media_type": p.Image.MediaType,
				"sha":        imageSha(p.Image.Data),
				"name":       p.Image.Name,
				"size":       len(p.Image.Data),
			})
		}
		if len(images) > 0 {
			payload["images"] = images
		}
		emit("USER_INPUT", payload)
	case "STEERING":
		text := joinText(turn.Message.Content)
		emit("STEERING_INJECTED", map[string]any{"text": text})
	case "ASSISTANT":
		for _, p := range turn.Message.Content {
			switch p.Kind {
			case "text":
				if p.Text == "" {
					continue
				}
				emit("ASSISTANT_TEXT_START", map[string]any{})
				emit("ASSISTANT_TEXT_END", map[string]any{
					"text":          p.Text,
					"finish_reason": "stop",
				})
			case "tool_call":
				if p.ToolCall == nil {
					continue
				}
				toolNames[p.ToolCall.ID] = p.ToolCall.Name
				args := ""
				if len(p.ToolCall.Arguments) > 0 {
					args = string(p.ToolCall.Arguments)
				}
				emit("TOOL_CALL_START", map[string]any{
					"tool_name":      p.ToolCall.Name,
					"call_id":        p.ToolCall.ID,
					"arguments_json": args,
				})
			}
		}
	case "TOOL", "TOOL_RESULTS":
		for _, p := range turn.Message.Content {
			if p.Kind != "tool_result" || p.ToolResult == nil {
				continue
			}
			name := p.ToolResult.Name
			if name == "" {
				name = toolNames[p.ToolResult.ToolCallID]
			}
			out := stringifyToolContent(p.ToolResult.Content)
			payload := map[string]any{
				"tool_name": name,
				"call_id":   p.ToolResult.ToolCallID,
			}
			if p.ToolResult.IsError {
				payload["error"] = out
			} else {
				payload["output"] = out
			}
			emit("TOOL_CALL_END", payload)
		}
	}
}

func joinText(parts []replayPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func stringifyToolContent(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
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

// spawnViewData is the template data for the spawn partial.
type spawnViewData struct {
	DefaultModel           string
	DefaultModelValue      string
	DefaultHarness         string
	DefaultWorkingDir      string
	DefaultWorkingDirValue string
	DefaultBranch          string
	DefaultBranchValue     string
	DefaultAccessMode      string
	DefaultPrompt          string // optional ?prompt= pre-fill
	RecentPrompts          []string
	Harnesses              []launchHarness
}

type launchHarness struct {
	ID    string
	Label string
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

func launchHarnessIDs(cfg WebConfig) []string {
	descriptors := launchHarnessDescriptors(cfg)
	out := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		out = append(out, descriptor.ID)
	}
	return out
}

// spawnRequest is the JSON body for POST /api/spawn. The prompt field is
// current; task is accepted by UnmarshalJSON for legacy callers.
type spawnRequest struct {
	Prompt          string `json:"prompt"`
	Harness         string `json:"harness"`
	Model           string `json:"model"`
	WorkingDir      string `json:"working_dir"`
	Branch          string `json:"branch"`
	AccessMode      string `json:"access_mode"`
	Agent           string `json:"agent"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func (r *spawnRequest) UnmarshalJSON(data []byte) error {
	type spawnRequestAlias spawnRequest
	var aux struct {
		spawnRequestAlias
		LegacyTask string `json:"task"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = spawnRequest(aux.spawnRequestAlias)
	if r.Prompt == "" {
		r.Prompt = aux.LegacyTask
	}
	return nil
}

// handleApiSpawn spawns a new daemon and optionally sends the initial prompt.
func (s *WebServer) handleApiSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req spawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.cfg.Spawner == nil && len(s.cfg.CodexSources) == 0 && len(s.cfg.CodexLaunches) == 0 {
		writeSpawnError(w, appwire.Unavailable("spawner not configured"))
		return
	}
	resp, err := hubThreadStart(r.Context(), s.cfg, s.sources, appwire.ThreadStartParams{
		Harness:         req.Harness,
		CWD:             req.WorkingDir,
		Prompt:          req.Prompt,
		Model:           req.Model,
		Profile:         req.Agent,
		ReasoningEffort: req.ReasoningEffort,
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

// handleApiModels returns the models the hub can spawn for. The list is what
// each configured provider's /models API returns RIGHT NOW (a live call to
// the provider, the same path the daemon's /models endpoint uses). Pricing
// and context-window metadata come from the embedded catalog where the live
// API doesn't carry it.
//
// Future: when the hub talks to a remote serf daemon, this handler will
// proxy to that daemon's /models. The cache here is shared across the
// hub's Spawn() target, which is local-only today.
func (s *WebServer) handleApiModels(w http.ResponseWriter, r *http.Request) {
	models := s.fetchLiveModels(r.Context())
	if len(models) == 0 {
		models = s.configuredModels()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models) //nolint:errcheck
}

func (s *WebServer) configuredModels() []map[string]any {
	if len(s.cfg.Models) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(s.cfg.Models))
	cat := llm.EmbeddedModelCatalog()
	for _, m := range s.cfg.Models {
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

// modelsCache holds a per-process cache of live ListModels results keyed by
// provider name, with a TTL. Provider /models calls are cheap but not free.
type modelsCache struct {
	mu      sync.Mutex
	expires time.Time
	models  []map[string]any
}

var liveModelsCache modelsCache

const liveModelsTTL = 5 * time.Minute

func (s *WebServer) fetchLiveModels(ctx context.Context) []map[string]any {
	liveModelsCache.mu.Lock()
	if time.Now().Before(liveModelsCache.expires) && liveModelsCache.models != nil {
		out := liveModelsCache.models
		liveModelsCache.mu.Unlock()
		return out
	}
	liveModelsCache.mu.Unlock()

	c, err := llm.NewFromEnv()
	if err != nil || c == nil {
		return nil
	}
	cat := llm.EmbeddedModelCatalog()

	var out []map[string]any
	for _, prov := range c.ProviderNames() {
		// Skip dual-route variants that surface the same models as their
		// primary route. openrouter-anthropic exists for specific models
		// whose tool-calling format requires the Anthropic-Messages endpoint,
		// but it lists the same /models response as plain openrouter. The
		// daemon picks the correct route based on the model name when
		// spawning; the picker doesn't need to expose both.
		if prov == "openrouter-anthropic" {
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
			if prov == "openrouter" && (mi == nil || !mi.SupportsTools) {
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
			// Enrich from the embedded catalog where the live API doesn't
			// carry context window or pricing.
			if mi != nil {
				entry["context_window"] = mi.ContextWindow
				entry["supports_tools"] = mi.SupportsTools
				entry["supports_reasoning"] = mi.SupportsReasoning
				entry["input_cost_per_million"] = mi.InputCostPerMillion
				entry["output_cost_per_million"] = mi.OutputCostPerMillion
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
	case "events":
		if !s.isLive(id) && s.cfg.Past != nil {
			if pe, ok := s.cfg.Past.Find(id); ok {
				s.serveReplay(w, r, pe)
				return
			}
		}
		s.serveAppwireEvents(w, r, id, true)
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

// WorkspaceData is the template data for the workspace partial.
type WorkspaceData struct {
	ID             string
	SourceLabel    string
	Title          string
	Branch         string
	WorkingDir     string
	State          string
	StateLabel     string
	TurnCount      int
	Model          string
	ContextWindow  int
	ContextPercent int
	ContextNumbers string
	Cost           string
	ReplayURL      string
	EventsURL      string
	Capabilities   hubapi.SessionCapabilities
	// Fork lineage for the preserved-original side of a fork. Non-empty
	// only when this session's meta carries ForkLabel — i.e., it's the
	// dim, snapshotted original. ForkOfTitle is the title of the new
	// branch (the session whose ParentSessionID == this.ID); empty if the
	// new branch is not in the past index.
	ForkLabel      string
	ForkOfTitle    string
	DivergenceTurn int
}

func (s *WebServer) renderWorkspacePartial(w http.ResponseWriter, r *http.Request, id string) {
	data := s.workspaceData(id)
	if data.ID == "" {
		http.NotFound(w, r)
		return
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
	fmt.Fprintln(w, `<header class="details-panel-header"><span>details</span><span class="details-panel-close">esc to close</span></header>`)
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
		source, err := sourceForThread(s.sources, ref, "")
		if err != nil {
			return WorkspaceData{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
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
			// A daemon reporting ENDED is still in the roster (its rendezvous
			// file lingers until pruned) but its /events stream will never
			// emit anything new. Treat it as past content: serve replay so
			// the user sees the full transcript. The renderer will switch
			// to /events on the next send-to-ended (which resumes a fresh
			// daemon).
			if state == "ended" && s.cfg.Past != nil {
				if _, ok := s.cfg.Past.Find(id); ok {
					data.ReplayURL = "/past/" + id + "/replay"
					return data
				}
			}
			data.Capabilities = s.liveWorkspaceCapabilities(id, data.Capabilities)
			data.EventsURL = "/s/" + id + "/events"
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
				ReplayURL:    "/past/" + id + "/replay",
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
		TurnCount:    len(thread.Turns),
		Model:        thread.ModelProvider,
		WorkingDir:   thread.CWD,
		EventsURL:    "/s/" + url.PathEscape(ref) + "/events",
		Capabilities: hubCapabilitiesFromAppwire(thread.Serf.Capabilities),
	}
}

func (s *WebServer) liveWorkspaceCapabilities(id string, fallback hubapi.SessionCapabilities) hubapi.SessionCapabilities {
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
	if err != nil {
		return fallback
	}
	caps := hubCapabilitiesFromAppwire(resp.Thread.Serf.Capabilities)
	caps.Resume = fallback.Resume
	return caps
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
			if candidate.OriginalPrompt != "" {
				data.ForkOfTitle = candidate.OriginalPrompt
			} else {
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
		if pe, ok := past.Find(id); ok && pe.Meta.OriginalPrompt != "" {
			return pe.Meta.OriginalPrompt
		}
	}
	return shortID(id)
}

func pastTitle(pe PastEntry) string {
	if pe.Meta.OriginalPrompt != "" {
		return pe.Meta.OriginalPrompt
	}
	return shortID(pe.Meta.ID)
}

func stateLabel(state string) string {
	switch state {
	case "awaiting":
		return "awaiting"
	case "processing":
		return "processing"
	case "warning":
		return "warning"
	case "idle":
		return "idle"
	case "ended":
		return "ended"
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
	}
	if data["Model"] == "" {
		data["Model"] = "—"
	}
	if detail, ok := s.apiSessionDetail(id); ok {
		if detail.Model != "" {
			data["Model"] = detail.Model
		}
		data["ContextPercent"] = int(detail.ContextPressure * 100)
		if detail.WorkingDir != "" {
			data["WorkingDir"] = detail.WorkingDir
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.inputStripTmpl.ExecuteTemplate(w, "input_status", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// sendRequest is the JSON body accepted by POST /s/<id>/send. The shape is
// wire-compatible with the daemon's server.InputRequest, so we forward by
// re-marshaling this struct directly.
type sendRequest struct {
	Text   string                  `json:"text"`
	Images []agent.ImageAttachment `json:"images,omitempty"`
}

// Per-request limits for image attachments. The browser-side cap is 8 MB per
// image; these are slightly looser to leave a margin while still protecting
// the hub from runaway uploads. A malicious or buggy client cannot push a
// session's transcript past sendMaxRequestBytes.
const (
	sendMaxImageBytes   = 12 * 1024 * 1024 // per-image
	sendMaxRequestBytes = 40 * 1024 * 1024 // total request body
)

func (s *WebServer) handleSend(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, sendMaxRequestBytes)
	var body sendRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Match the daemon's validation: at least one of text or images is required.
	if body.Text == "" && len(body.Images) == 0 {
		http.Error(w, "text or images required", http.StatusBadRequest)
		return
	}
	for i, img := range body.Images {
		if len(img.Data) > sendMaxImageBytes {
			http.Error(w, fmt.Sprintf("image[%d] %q exceeds %d-byte limit", i, img.Name, sendMaxImageBytes), http.StatusRequestEntityTooLarge)
			return
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
		entry, err := s.cfg.Spawner.Resume(r.Context(), s.resumeRequestFor(id))
		if err != nil {
			return LiveEntry{}, fmt.Errorf("resume: %w", err)
		}
		le := waitForRosterMatch(s.cfg.Roster, id, entry.PID, 5*time.Second)
		if le.Address == "" {
			return LiveEntry{}, fmt.Errorf("daemon not in roster after resume")
		}
		return le, nil
	}

	ref := appRefFromRouteID(id)
	turnParams := appwire.TurnStartParams{Ref: ref, Prompt: body.Text}
	for _, img := range body.Images {
		turnParams.Items = append(turnParams.Items, appwire.InputItem{
			Type:      "image",
			MediaType: img.MediaType,
			Data:      img.Data,
			Name:      img.Name,
		})
	}
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
		_, err = source.StartTurn(r.Context(), turnParams)
		return err
	}

	if err := startTurn(false); err != nil {
		if strings.Contains(err.Error(), "spawner not configured") {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
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

func (s *WebServer) resumeRequestFor(id string) ResumeRequest {
	return resumeRequestForConfig(s.cfg, id)
}

// steerRequest is the JSON body for POST /s/<id>/steer.
type steerRequest struct {
	Text string `json:"text"`
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
	if err := source.SteerTurn(r.Context(), appwire.TurnSteerParams{Ref: ref, Text: body.Text}); err != nil {
		writeSessionActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionAction forwards an imperative action (interrupt/compact/
// shutdown) to the live daemon for the given session. Unlike /send, these
// actions do NOT auto-resume an ended session: if there is no roster entry
// the action is a no-op and we return 404. The daemon's status code (204 or
// 202) is forwarded to the client.
func (s *WebServer) handleSessionAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
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
	ref := appRefFromRouteID(id)
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "interrupt":
		err = source.InterruptTurn(r.Context(), appwire.TurnInterruptParams{Ref: ref})
	case "compact":
		err = source.CompactThread(r.Context(), appwire.ThreadCompactStartParams{Ref: ref})
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
	}
	http.Error(w, err.Error(), status)
}

type forkRequest struct {
	Turn          int    `json:"turn"`
	EditedMessage string `json:"edited_message"`
	Label         string `json:"label"`
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

// daemonStatus is the subset of /status fields the hub cares about.
type daemonStatus struct {
	SessionID       string  `json:"session_id"`
	Model           string  `json:"model"`
	Profile         string  `json:"profile"`
	State           string  `json:"state"`
	Turns           int     `json:"turns"`
	WorkingDir      string  `json:"working_dir,omitempty"`
	ContextPressure float64 `json:"context_pressure"`
	ContextUsed     int     `json:"context_used,omitempty"`
	ContextWindow   int     `json:"context_window,omitempty"`
}

// fetchStatus reads /status from the daemon at le.Address, returning nil on any error.
func (s *WebServer) fetchStatus(le LiveEntry) *daemonStatus {
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get("http://" + le.Address + "/status") //nolint:gosec
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
