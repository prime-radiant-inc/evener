package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// noStore wraps a handler to forbid caching — used for on-disk dev assets so
// edits always reload (browsers otherwise heuristically cache un-headered CSS/JS).
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

var (
	assetVersionOnce sync.Once
	assetVersionVal  string
)

// assetVersionQuery returns a "?v=<token>" cache-busting suffix appended to
// every /assets URL in the page templates. The token is the binary's build
// time, so it changes on every rebuild/deploy (forcing browsers to fetch the
// new CSS/JS) but stays stable across mere restarts of the same binary. In the
// on-disk dev mode the served assets are no-store, so the (stale) token there
// is harmless.
func assetVersionQuery() string {
	assetVersionOnce.Do(func() {
		token := "1"
		if exe, err := os.Executable(); err == nil {
			if fi, err := os.Stat(exe); err == nil {
				token = strconv.FormatInt(fi.ModTime().Unix(), 10)
			}
		}
		assetVersionVal = "?v=" + token
	})
	return assetVersionVal
}

//go:embed templates/*.html templates/partials/*.html templates/partials/settings/*.html
var templatesFS embed.FS

//go:embed assets/*
var assetsFS embed.FS

// devAssetsDir is the optional on-disk source root (containing assets/ and
// templates/) set via SERF_HUB_ASSETS_DIR. When set, the hub serves assets and
// re-parses templates from disk on each request so CSS/JS/template edits take
// effect on reload without rebuilding the binary. Empty in normal operation.
func devAssetsDir() string {
	return os.Getenv("SERF_HUB_ASSETS_DIR")
}

// templatesRoot returns the fs.FS to parse templates from: the on-disk dev
// source when SERF_HUB_ASSETS_DIR is set, otherwise the embedded copy.
func templatesRoot() fs.FS {
	if dir := devAssetsDir(); dir != "" {
		return os.DirFS(dir)
	}
	return templatesFS
}

// assetsRoot returns the fs.FS rooted at the assets/ directory: on-disk when
// SERF_HUB_ASSETS_DIR is set, otherwise the embedded copy.
func assetsRoot() fs.FS {
	if dir := devAssetsDir(); dir != "" {
		return os.DirFS(filepath.Join(dir, "assets"))
	}
	sub, _ := fs.Sub(assetsFS, "assets")
	return sub
}
