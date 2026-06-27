package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

// noStore wraps a handler to forbid caching — used for on-disk dev assets so
// edits always reload (browsers otherwise heuristically cache un-headered CSS/JS).
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
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
