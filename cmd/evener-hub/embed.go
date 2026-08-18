package main

import (
	"embed"
	"io/fs"
)

//go:embed assets/*
var assetsFS embed.FS

// assetsRoot returns the fs.FS rooted at the embedded assets/ directory — the
// PWA icons and manifest.webmanifest.
func assetsRoot() fs.FS {
	sub, _ := fs.Sub(assetsFS, "assets")
	return sub
}
