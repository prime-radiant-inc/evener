package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/rendezvous"
)

// WebConfig is everything the web server needs.
type WebConfig struct {
	HubAddr     string
	Roster      *Roster
	Past        *PastIndex
	Spawner     Spawner // optional; nil disables /live/new
	PastPerPage int     // results per page for /past; defaults to 50 when zero
	StateDir    string  // root of the projects/<sha> state directory; needed for ForkSession
}

// Spawner forks a serf serve subprocess from a SpawnTemplate and waits for
// its rendezvous file to appear. Returns the discovered Entry's Address
// (the daemon's bound port) on success.
type Spawner interface {
	Spawn(ctx context.Context, templateName, workingDir string) (rendezvous.Entry, error)
	Resume(ctx context.Context, sessionID string) (rendezvous.Entry, error)
	Templates() []SpawnTemplate
}

// WebServer wires routes, templates, and middleware.
type WebServer struct {
	cfg             WebConfig
	templates       *template.Template
	appTmpl         *template.Template
	sidebarTmpl     *template.Template
	liveTmpl        *template.Template
	liveNewTmpl     *template.Template
	pastTmpl        *template.Template
	pastResultsTmpl *template.Template
	pastViewTmpl    *template.Template
	workspaceTmpl   *template.Template
	inputStripTmpl  *template.Template
	rest            *RESTProxy
	sse             *SSEProxy

	resumeMu    sync.Mutex
	resumeLocks map[string]*sync.Mutex // sessionID -> per-session lock
}

// NewWebServer constructs the web server. Templates are parsed from embed.FS.
func NewWebServer(cfg WebConfig) *WebServer {
	tmpl := template.Must(template.ParseFS(templatesFS,
		"templates/base.html",
		"templates/landing.html",
		"templates/partials/live_roster.html",
	))
	appTmpl := template.Must(template.ParseFS(templatesFS, "templates/app.html"))
	sidebarTmpl := template.Must(template.ParseFS(templatesFS, "templates/partials/sidebar.html"))
	liveTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/base.html",
		"templates/live.html",
		"templates/partials/status_bar.html",
	))
	liveNewTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/base.html",
		"templates/live_new.html",
	))
	pastTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/base.html",
		"templates/past.html",
		"templates/partials/past_results.html",
	))
	pastResultsTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/past_results.html",
	))
	pastViewTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/base.html",
		"templates/past_view.html",
	))
	workspaceTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/workspace.html",
	))
	inputStripTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/partials/input_strip.html",
	))
	var rest *RESTProxy
	var sse *SSEProxy
	if cfg.Roster != nil {
		rest = NewRESTProxy(cfg.Roster)
		sse = NewSSEProxy(cfg.Roster)
	}
	return &WebServer{
		cfg: cfg, templates: tmpl, appTmpl: appTmpl, sidebarTmpl: sidebarTmpl,
		liveTmpl: liveTmpl, liveNewTmpl: liveNewTmpl,
		pastTmpl: pastTmpl, pastResultsTmpl: pastResultsTmpl, pastViewTmpl: pastViewTmpl,
		workspaceTmpl: workspaceTmpl, inputStripTmpl: inputStripTmpl,
		rest: rest, sse: sse,
		resumeLocks: map[string]*sync.Mutex{},
	}
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

	// Pages
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/s/", s.handleSession)   // workspace — detects HX-Request and serves partial or app shell
	mux.HandleFunc("/new", s.handleIndex)    // ditto
	mux.HandleFunc("/sidebar", s.handleSidebar)
	mux.HandleFunc("/workspace/empty", s.handleWorkspaceEmpty)
	mux.HandleFunc("/live", s.handleLiveRoster)
	mux.HandleFunc("/live/new", s.handleLiveNew) // must be before /live/
	mux.HandleFunc("/past", s.handlePast)
	mux.HandleFunc("/past/results", s.handlePastResults) // must be before /past/
	mux.HandleFunc("/past/", s.handlePastID)

	// Live drive proxied routes (REST and SSE)
	mux.HandleFunc("/live/", s.handleLiveProxy)

	guard := SameOriginGuard(s.cfg.HubAddr)
	return guard(CSPMiddleware(mux))
}

func (s *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/new" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.appTmpl.ExecuteTemplate(w, "app", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	fmt.Fprint(w, `<div class="workspace-empty"><p>No session selected.</p><p style="margin-top:1em"><a href="/new" hx-get="/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ new session</a></p></div>`)
}

func (s *WebServer) handleLiveRoster(w http.ResponseWriter, r *http.Request) {
	var live []LiveEntry
	if s.cfg.Roster != nil {
		live = s.cfg.Roster.List()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "live_roster", map[string]any{"Live": live}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) handlePast(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if s.cfg.Past == nil {
		http.Error(w, "no past index", http.StatusServiceUnavailable)
		return
	}
	limit := s.cfg.PastPerPage
	if limit <= 0 {
		limit = 50
	}
	results := s.cfg.Past.Search(q, limit, 0)
	data := map[string]any{
		"Title": "past",
		"Past":  results,
		"Q":     q,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pastTmpl.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePastResults renders just the past_results partial (htmx swap target).
// The /past page form posts here on input changes and on submit.
func (s *WebServer) handlePastResults(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Past == nil {
		http.Error(w, "no past index", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query().Get("q")
	limit := s.cfg.PastPerPage
	if limit <= 0 {
		limit = 50
	}
	results := s.cfg.Past.Search(q, limit, 0)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pastResultsTmpl.ExecuteTemplate(w, "past_results", map[string]any{"Past": results}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleLiveProxy dispatches /live/<session_id>/* to the appropriate proxy.
// Reserved as a placeholder; concrete drive page handler is added in Task 16.
func (s *WebServer) handleLiveProxy(w http.ResponseWriter, r *http.Request) {
	// /live/<session_id>/events -> SSE proxy
	// /live/<session_id>/<other> -> REST proxy
	// /live/<session_id> -> drive page (Task 16)
	path := r.URL.Path
	if path == "/live" || path == "/live/" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if sessID, rest, ok := splitLivePath(path); ok {
		switch {
		case rest == "events":
			if s.sse == nil {
				http.NotFound(w, r)
				return
			}
			s.sse.ServeHTTP(w, r)
		case rest == "":
			entry, ok := s.cfg.Roster.Find(sessID)
			if !ok {
				http.NotFound(w, r)
				return
			}
			data := map[string]any{
				"Title":     "drive",
				"SessionID": entry.SessionID,
				"Entry":     entry,
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := s.liveTmpl.ExecuteTemplate(w, "base", data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case rest == "status-bar":
			s.handleStatusBar(w, r, sessID)
		default:
			if s.rest == nil {
				http.NotFound(w, r)
				return
			}
			s.rest.ServeHTTP(w, r)
		}
		return
	}
	http.NotFound(w, r)
}

// handleStatusBar fetches /status from the daemon, decodes a subset of fields,
// and renders the status_bar partial as an htmx fragment.
func (s *WebServer) handleStatusBar(w http.ResponseWriter, r *http.Request, sessID string) {
	entry, ok := s.cfg.Roster.Find(sessID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	statusURL := "http://" + entry.Address + "/status"
	client := &http.Client{Timeout: 1 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, statusURL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := client.Do(req) //nolint:gosec // address comes from trusted roster
	if err != nil {
		http.Error(w, "daemon unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var info struct {
		Model           string  `json:"model"`
		Profile         string  `json:"profile"`
		State           string  `json:"state"`
		Turns           int     `json:"turns"`
		ContextPressure float64 `json:"context_pressure"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.liveTmpl.ExecuteTemplate(w, "status_bar", info); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePastID dispatches /past/<id>, /past/<id>/replay, and /past/<id>/resume.
func (s *WebServer) handlePastID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/past/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		http.Redirect(w, r, "/past", http.StatusFound)
		return
	}
	if s.cfg.Past == nil {
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
	switch tail {
	case "":
		data := map[string]any{
			"Title":    "past",
			"Meta":     entry.Meta,
			"StateDir": entry.StateDir,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.pastViewTmpl.ExecuteTemplate(w, "base", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "resume":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.cfg.Spawner == nil {
			http.Error(w, "resume not configured", http.StatusServiceUnavailable)
			return
		}
		lock := s.lockForSession(id)
		lock.Lock()
		defer lock.Unlock()
		entry, err := s.cfg.Spawner.Resume(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sessID := s.findNewSession(entry.PID); sessID != "" {
			http.Redirect(w, r, "/live/"+sessID, http.StatusFound)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	case "replay":
		s.serveReplay(w, r, entry)
	default:
		http.NotFound(w, r)
	}
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
		if head.Kind != "entry" {
			// Skip api_call records and unknown top-level kinds.
			continue
		}
		var entryRec replayEntry
		if err := json.Unmarshal(raw, &entryRec); err != nil {
			continue
		}
		emitTurnEvents(emit, entryRec.Turn, toolNames)
	}
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
	Kind    string         `json:"kind"`
	Message replayMessage  `json:"message"`
}

type replayMessage struct {
	Role    string         `json:"role"`
	Content []replayPart   `json:"content"`
}

type replayPart struct {
	Kind       string             `json:"kind"`
	Text       string             `json:"text,omitempty"`
	ToolCall   *replayToolCall    `json:"tool_call,omitempty"`
	ToolResult *replayToolResult  `json:"tool_result,omitempty"`
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
func emitTurnEvents(emit func(string, any), turn replayTurn, toolNames map[string]string) {
	switch turn.Kind {
	case "USER_INPUT":
		text := joinText(turn.Message.Content)
		emit("USER_INPUT", map[string]any{"text": text})
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

// handleLiveNew serves GET /live/new (spawn form) and POST /live/new (spawn action).
func (s *WebServer) handleLiveNew(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var templates []SpawnTemplate
		if s.cfg.Spawner != nil {
			templates = s.cfg.Spawner.Templates()
		}
		data := map[string]any{
			"Title":     "spawn",
			"Templates": templates,
			"Live":      []LiveEntry{},
			"Past":      []PastEntry{},
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.liveNewTmpl.ExecuteTemplate(w, "base", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case http.MethodPost:
		if s.cfg.Spawner == nil {
			http.Error(w, "spawn not configured", http.StatusServiceUnavailable)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tmpl := r.FormValue("template")
		wd := r.FormValue("working_dir")
		if wd != "" {
			if !filepath.IsAbs(wd) {
				http.Error(w, "working_dir must be an absolute path", http.StatusBadRequest)
				return
			}
			info, err := os.Stat(wd)
			if err != nil || !info.IsDir() {
				http.Error(w, "working_dir does not exist or is not a directory", http.StatusBadRequest)
				return
			}
		}
		entry, err := s.cfg.Spawner.Spawn(r.Context(), tmpl, wd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sessID := s.findNewSession(entry.PID); sessID != "" {
			http.Redirect(w, r, "/live/"+sessID, http.StatusFound)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSession is the router for all /s/<id>[/<sub>] routes.
// When HX-Request is true and sub is "", it serves the workspace partial;
// otherwise it serves the app shell for full-page navigation.
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
			s.renderWorkspacePartial(w, r, id)
			return
		}
		// Full-page navigation: serve the app shell.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.appTmpl.ExecuteTemplate(w, "app", nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "state":
		s.renderInputStrip(w, r, id)
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
	case "events":
		if s.sse == nil {
			http.NotFound(w, r)
			return
		}
		// Rewrite the path so the SSE proxy's splitLivePath can parse it.
		r.URL.Path = "/live/" + id + "/events"
		s.sse.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// WorkspaceData is the template data for the workspace partial.
type WorkspaceData struct {
	ID             string
	Title          string
	Branch         string
	State          string
	StateLabel     string
	TurnCount      int
	Model          string
	ContextPercent int
	ContextNumbers string
	Cost           string
	ReplayURL      string
	EventsURL      string
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

func (s *WebServer) workspaceData(id string) WorkspaceData {
	if s.cfg.Roster != nil {
		if le, ok := s.cfg.Roster.Find(id); ok {
			return WorkspaceData{
				ID:        id,
				Title:     liveTitle(le),
				State:     le.Status,
				StateLabel: stateLabel(le.Status),
				Model:     le.Model,
				EventsURL: "/s/" + id + "/events",
			}
		}
	}
	if s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			return WorkspaceData{
				ID:         id,
				Title:      pastTitle(pe),
				State:      "ended",
				StateLabel: stateLabel("ended"),
				TurnCount:  pe.Meta.TurnCount,
				Model:      pe.Meta.Model,
				ReplayURL:  "/past/" + id + "/replay",
			}
		}
	}
	return WorkspaceData{}
}

func liveTitle(le LiveEntry) string {
	return le.SessionID
}

func pastTitle(pe PastEntry) string {
	if pe.Meta.OriginalTask != "" {
		return pe.Meta.OriginalTask
	}
	return pe.Meta.ID
}

func stateLabel(state string) string {
	switch state {
	case "awaiting":
		return "● awaiting"
	case "processing":
		return "● processing"
	case "warning":
		return "● warning"
	case "idle":
		return "● idle"
	case "ended":
		return "ended"
	}
	return state
}

func (s *WebServer) renderInputStrip(w http.ResponseWriter, r *http.Request, id string) {
	data := map[string]any{
		"Model":          "—",
		"ContextPercent": 0,
		"ContextNumbers": "",
		"Cost":           "",
	}
	if s.cfg.Roster != nil {
		if le, ok := s.cfg.Roster.Find(id); ok {
			if info := s.fetchStatus(le); info != nil {
				data["Model"] = info.Model
				data["ContextPercent"] = int(info.ContextPressure * 100)
				if info.ContextUsed > 0 || info.ContextWindow > 0 {
					data["ContextNumbers"] = fmt.Sprintf("%d/%d", info.ContextUsed, info.ContextWindow)
				}
			}
		}
	}
	if data["Model"] == "—" && s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(id); ok {
			data["Model"] = pe.Meta.Model
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.inputStripTmpl.ExecuteTemplate(w, "input_strip", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *WebServer) handleSend(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var le LiveEntry
	if s.cfg.Roster != nil {
		var ok bool
		le, ok = s.cfg.Roster.Find(id)
		if !ok {
			// Session is not live — try to resume it.
			if s.cfg.Spawner == nil {
				http.Error(w, "spawner not configured", http.StatusServiceUnavailable)
				return
			}
			lock := s.lockForSession(id)
			lock.Lock()
			defer lock.Unlock()
			// Re-check after acquiring the lock — another request may have resumed it.
			if le2, ok2 := s.cfg.Roster.Find(id); ok2 {
				le = le2
			} else {
				entry, err := s.cfg.Spawner.Resume(r.Context(), id)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				le = waitForRosterMatch(s.cfg.Roster, id, entry.PID, 3*time.Second)
				if le.Address == "" {
					http.Error(w, "daemon not in roster after resume", http.StatusInternalServerError)
					return
				}
			}
		}
	} else {
		http.Error(w, "spawner not configured", http.StatusServiceUnavailable)
		return
	}

	// Forward the message to the daemon's /input endpoint.
	payload, _ := json.Marshal(map[string]string{"text": body.Text})
	daemonURL := "http://" + le.Address + "/input"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, daemonURL, bytes.NewReader(payload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

func (s *WebServer) handleFork(w http.ResponseWriter, r *http.Request, parentID string) {
	var body struct {
		Turn          int    `json:"turn"`
		EditedMessage string `json:"edited_message"`
		Label         string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Resolve the state dir for the parent session.
	stateDir := s.cfg.StateDir
	if stateDir == "" && s.cfg.Past != nil {
		if pe, ok := s.cfg.Past.Find(parentID); ok {
			stateDir = pe.StateDir
		}
	}
	if stateDir == "" {
		http.Error(w, "state dir not configured or session not found", http.StatusInternalServerError)
		return
	}

	childID, err := agent.ForkSession(stateDir, parentID, body.Turn, body.EditedMessage, body.Label)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Refresh past index so the new session shows up immediately in the sidebar.
	if s.cfg.Past != nil {
		_ = s.cfg.Past.Rebuild()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"child_session_id": childID}) //nolint:errcheck
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
	Model           string  `json:"model"`
	Profile         string  `json:"profile"`
	State           string  `json:"state"`
	Turns           int     `json:"turns"`
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
