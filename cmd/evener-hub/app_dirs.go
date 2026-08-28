package hub

import (
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars"
)

// hubDirsCreate creates the working directory requested by Spawn. Stat errors
// intentionally fall through to MkdirAll: the existing route treated an
// inaccessible path the same as a missing path and let the filesystem return
// the user-visible creation error.
func hubDirsCreate(cfg hubcore.WebConfig, params appwire.DirsCreateParams) (appwire.DirsCreateResponse, error) {
	path := strings.TrimSpace(params.Path)
	if path == "" {
		return appwire.DirsCreateResponse{}, appwire.InvalidParams("path is required")
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		path = filepath.Join(envvars.Home.Getenv(), strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		return appwire.DirsCreateResponse{}, appwire.InvalidParams("absolute path required")
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return appwire.DirsCreateResponse{}, appwire.Conflict("a file already exists at that path")
		}
		return appwire.DirsCreateResponse{Path: path, Created: false}, nil
	}

	mkdirAll := os.MkdirAll
	if cfg.MkdirAll != nil {
		mkdirAll = cfg.MkdirAll
	}
	if err := mkdirAll(path, 0o755); err != nil {
		return appwire.DirsCreateResponse{}, appwire.InternalError(err.Error())
	}
	return appwire.DirsCreateResponse{Path: path, Created: true}, nil
}
