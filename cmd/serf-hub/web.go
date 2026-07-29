package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
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

// WebServer wires routes and middleware.
type WebServer struct {
	cfg       hubcore.WebConfig
	appRPC    *appserver.Server
	sources   *appsource.Registry
	startedAt time.Time

	// lastGoodThreads retains each remote source's most recent successful
	// ListThreads result so a transient list failure doesn't blank that
	// source's sessions from the sidebar (which renders a snapshot).
	lastGoodMu      sync.Mutex
	lastGoodThreads map[string][]appwire.Thread // sourceID -> last successful list
	// remoteFetchGeneration invalidates synchronous no-cache tree memoization.
	// Authority metadata itself stays request-owned in navigationSnapshot.
	remoteFetchGeneration atomic.Uint64
	// liveModels caches raw live /models listings for this server; per-server
	// so another WebServer (different provider config) never shares entries.
	liveModels *modelsCache
	// treeCache memoizes the complete /api/tree navigation generation — tree,
	// attention, live entries, and favorite authority — by inputs-version,
	// remote generation, and 30s time bucket (see hubcore.TreeCache).
	treeCache  *hubcore.TreeCache
	manifestFS fs.FS

	deletionStoreErr error
}

var manifestMarshal = json.Marshal

// NewWebServer constructs the web server.
func NewWebServer(cfg hubcore.WebConfig) *WebServer {
	var deletionStoreErr error
	if cfg.DeletionStore == nil {
		cfg.DeletionStore, deletionStoreErr = hubcore.NewDeletionStore(cfg.HubStateRoot)
	}
	sources := newHubSourceRegistry(cfg)
	if cfg.CodexLauncher == nil && len(cfg.CodexLaunches) > 0 {
		cfg.CodexLauncher = codexlaunch.NewCodexLauncher(cfg.CodexLaunches)
	}
	// One resume-lock registry backs both the REST send path (lockForSession)
	// and the RPC auto-resume path (hubThreadResume via cfg), so a resume
	// triggered on either transport serializes a racing resume on the other.
	if cfg.ResumeLocks == nil {
		cfg.ResumeLocks = hubcore.NewResumeLocks()
	}
	web := &WebServer{
		cfg:              cfg,
		sources:          sources,
		startedAt:        time.Now().UTC(),
		lastGoodThreads:  map[string][]appwire.Thread{},
		liveModels:       &modelsCache{},
		treeCache:        &hubcore.TreeCache{},
		manifestFS:       assetsRoot(),
		deletionStoreErr: deletionStoreErr,
	}
	web.appRPC = newHubAppServer(cfg, sources)
	if deletionStoreErr == nil {
		_ = web.resumeProjectDeletions()
	}
	return web
}

// lockForSession returns (creating if necessary) the per-session mutex for
// serializing concurrent resume requests on the same session_id. It shares the
// registry the RPC auto-resume path uses (cfg.ResumeLocks) so the two paths
// serialize against each other.
func (s *WebServer) lockForSession(sessionID string) *sync.Mutex {
	if s.cfg.ResumeLocks == nil {
		s.cfg.ResumeLocks = hubcore.NewResumeLocks()
	}
	return s.cfg.ResumeLocks.For(sessionID)
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

	// Assets — the embedded PWA icons + manifest, auth-exempt per hubedge.
	assetHandler := http.StripPrefix("/assets/", validAssetPath(http.FileServer(http.FS(assetsRoot()))))
	mux.Handle("/assets/", assetHandler)

	// Rewritten SPA's hashed Vite output (same auth-guard wrapping below; hashed
	// filenames are immutable, so aggressive caching is safe).
	mux.Handle("/webassets/", webassetsHandler(distFS()))

	// Same-origin blank shell dockview opens for a popped-out pane (its default
	// popoutUrl). Always registered — inert, and only the SPA's dockview loads
	// it; auth-gated by the guard below.
	mux.HandleFunc("/popout.html", servePopoutShell)

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

// handleIndex serves the SPA shell for "/", "/new", and every other page route
// the mux doesn't match more specifically; client-side routing owns the path
// (including the ?dir=/?prompt= spawn pre-fill the SPA reads itself).
func (s *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	serveSPAIndex(w, r, distFS())
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
