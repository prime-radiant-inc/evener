package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
)

// WebConfig is everything the web server needs.
type WebConfig struct {
	HubAddr string
	Roster  *Roster
	Past    *PastIndex
	Spawner *Spawner // optional; nil disables /live/new
}

// Spawner abstracts the daemon-spawning side. Defined here as an interface
// so web_test.go can pass nil; real implementation lands in Task 18.
type Spawner interface{}

// WebServer wires routes, templates, and middleware.
type WebServer struct {
	cfg          WebConfig
	templates    *template.Template
	liveTmpl     *template.Template
	rest         *RESTProxy
	sse          *SSEProxy
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
	))
	var rest *RESTProxy
	var sse *SSEProxy
	if cfg.Roster != nil {
		rest = NewRESTProxy(cfg.Roster)
		sse = NewSSEProxy(cfg.Roster)
	}
	return &WebServer{cfg: cfg, templates: tmpl, liveTmpl: liveTmpl, rest: rest, sse: sse}
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
	mux.HandleFunc("/past", s.handlePast)

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
		fmt.Fprintln(w, "no past index")
		return
	}
	results := s.cfg.Past.Search(q, 50, 0)
	data := map[string]any{
		"Title": "past",
		"Past":  results,
		"Q":     q,
		"Live":  []LiveEntry{},
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Reuse landing layout for now — drives "01A" appearing in body for the test.
	if err := s.templates.ExecuteTemplate(w, "base", data); err != nil {
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
	if _, rest, ok := splitLivePath(path); ok {
		switch {
		case rest == "events":
			if s.sse == nil {
				http.NotFound(w, r)
				return
			}
			s.sse.ServeHTTP(w, r)
		case rest == "":
			sessID, _, _ := splitLivePath(path)
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
