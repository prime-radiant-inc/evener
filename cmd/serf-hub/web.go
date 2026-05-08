package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/rendezvous"
)

// WebConfig is everything the web server needs.
type WebConfig struct {
	HubAddr     string
	Roster      *Roster
	Past        *PastIndex
	Spawner     Spawner // optional; nil disables /live/new
	PastPerPage int     // results per page for /past; defaults to 50 when zero
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
	cfg          WebConfig
	templates    *template.Template
	liveTmpl     *template.Template
	liveNewTmpl  *template.Template
	pastTmpl     *template.Template
	pastViewTmpl *template.Template
	rest         *RESTProxy
	sse          *SSEProxy

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
	))
	pastViewTmpl := template.Must(template.ParseFS(templatesFS,
		"templates/base.html",
		"templates/past_view.html",
	))
	var rest *RESTProxy
	var sse *SSEProxy
	if cfg.Roster != nil {
		rest = NewRESTProxy(cfg.Roster)
		sse = NewSSEProxy(cfg.Roster)
	}
	return &WebServer{
		cfg: cfg, templates: tmpl, liveTmpl: liveTmpl, liveNewTmpl: liveNewTmpl,
		pastTmpl: pastTmpl, pastViewTmpl: pastViewTmpl, rest: rest, sse: sse,
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
	mux.HandleFunc("/live", s.handleLiveRoster)
	mux.HandleFunc("/live/new", s.handleLiveNew) // must be before /live/
	mux.HandleFunc("/past", s.handlePast)
	mux.HandleFunc("/past/", s.handlePastID)

	// Live drive proxied routes (REST and SSE)
	mux.HandleFunc("/live/", s.handleLiveProxy)

	guard := SameOriginGuard(s.cfg.HubAddr)
	return guard(mux)
}

func (s *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	var live []LiveEntry
	if s.cfg.Roster != nil {
		live = s.cfg.Roster.List()
	}
	var past []PastEntry
	if s.cfg.Past != nil {
		past = s.cfg.Past.Search("", 10, 0)
	}
	data := map[string]any{
		"Title": "live",
		"Live":  live,
		"Past":  past,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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

// serveReplay reads <state_dir>/sessions/<id>.transcript.jsonl and streams it
// as SSE events, one per line. Each line must be JSON with "kind" and "data" fields.
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
	for scanner.Scan() {
		var ev struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		id++
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, ev.Kind, string(ev.Data))
		flusher.Flush()
	}
}

// findNewSession polls the roster up to 3s for a daemon with the given pid.
// Returns the resolved session_id when found, or empty string on timeout.
func (s *WebServer) findNewSession(pid int) string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.cfg.Roster != nil {
			s.cfg.Roster.refresh()
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
