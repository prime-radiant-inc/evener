package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:frontend/dist
var frontendDistFS embed.FS

// distFS returns the built frontend, a fs.FS seam so tests can substitute one.
var distFS = func() fs.FS {
	sub, err := fs.Sub(frontendDistFS, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// serveSPAIndex serves the app shell for every page route: client routing owns
// the path. 503s with instructions when the frontend was never built.
func serveSPAIndex(w http.ResponseWriter, _ *http.Request, dist fs.FS) {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("serf-hub web app not built: run `make build-web` and rebuild\n"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// popoutShellHTML is the minimal same-origin document dockview opens for a
// popped-out pane. dockview-core waits for this page's `load`, appends the
// popped group into its document.body, and clones the opener's stylesheets
// into it (popoutWindow.js:128-136; dom.js addStyles) — so the shell carries
// no app CSS or JS of its own, only a body to append into and the app's
// identity. A bare SPA fallback here would instead return index.html and boot
// a second full app in the popout window, which is the failure this route
// exists to prevent.
const popoutShellHTML = `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>serf</title></head><body></body></html>`

// servePopoutShell serves popoutShellHTML at dockview's default /popout.html.
// It is auth-gated like every other route (NOT in hubedge.isAuthExempt): a
// same-origin window.open carries the SameSite=Lax session cookie, so an
// authorized browser loads it and an anonymous client is refused. Registered
// unconditionally (like /webassets/) — the document is inert and only the
// rewritten SPA's dockview ever requests it.
func servePopoutShell(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(popoutShellHTML))
}

// webassetsHandler serves the hashed Vite output rooted at dist/webassets/.
// Hashed filenames are immutable by construction, so far-future caching is
// correct. Only the leading "/" is stripped from the request path — not
// "/webassets/" — because Vite emits into dist/webassets/, so the fs.FS
// lookup path for "/webassets/x.js" must stay "webassets/x.js" relative to
// dist, keeping the "webassets" segment intact.
func webassetsHandler(dist fs.FS) http.Handler {
	inner := http.StripPrefix("/", http.FileServerFS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		inner.ServeHTTP(w, r)
	})
}
