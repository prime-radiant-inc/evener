package fspaths

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/serf/appwire"
)

func CompleteDirs(params appwire.DirsCompleteParams) (appwire.DirsCompleteResponse, error) {
	prefix := params.Prefix
	if prefix == "" {
		prefix = os.Getenv("HOME")
	}
	if strings.HasPrefix(prefix, "~/") || prefix == "~" {
		prefix = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(prefix, "~"))
	}
	cleaned, err := SanitizeDirPrefix(prefix)
	if err != nil {
		return appwire.DirsCompleteResponse{}, nil
	}
	prefix = cleaned

	var listDir, filter string
	if strings.HasSuffix(prefix, string(filepath.Separator)) || prefix == "" {
		listDir = prefix
		if listDir == "" {
			listDir = string(filepath.Separator)
		}
	} else {
		listDir = filepath.Dir(prefix)
		filter = filepath.Base(prefix)
	}

	entries, err := os.ReadDir(listDir)
	if err != nil {
		return appwire.DirsCompleteResponse{}, nil
	}
	limit := params.Limit
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	results := make([]string, 0, limit)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") && filter == "" {
			continue
		}
		if filter != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(filter)) {
			continue
		}
		results = append(results, filepath.Join(listDir, name))
		if len(results) >= limit {
			break
		}
	}
	sort.Strings(results)
	return appwire.DirsCompleteResponse{Data: results}, nil
}

func ValidateLaunchPath(params appwire.PathValidateParams) appwire.PathValidateResponse {
	path := strings.TrimSpace(params.Path)
	kind := strings.TrimSpace(params.Kind)
	if path == "" {
		return appwire.PathValidateResponse{Valid: false, Error: "path is required"}
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		path = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(path, "~"))
	}
	if kind == "command" && !strings.ContainsRune(path, filepath.Separator) {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: err.Error()}
		}
		return appwire.PathValidateResponse{Path: resolved, Valid: true}
	}
	if !filepath.IsAbs(path) {
		return appwire.PathValidateResponse{Path: path, Valid: false, Error: "absolute path required"}
	}
	if kind == "output-file" {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "path is a directory"}
		}
		parent := filepath.Dir(path)
		info, err := os.Stat(parent)
		if err != nil {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: err.Error()}
		}
		if !info.IsDir() {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "parent path is not a directory"}
		}
		if info.Mode().Perm()&0o222 == 0 {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "parent directory is not writable"}
		}
		return appwire.PathValidateResponse{Path: path, Valid: true}
	}
	info, err := os.Stat(path)
	if err != nil {
		return appwire.PathValidateResponse{Path: path, Valid: false, Error: err.Error()}
	}
	switch kind {
	case "", "any":
	case "command":
		if info.IsDir() {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "path is a directory"}
		}
		if info.Mode()&0o111 == 0 {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "path is not executable"}
		}
	case "dir":
		if !info.IsDir() {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "path is not a directory"}
		}
	case "file":
		if info.IsDir() {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "path is a directory"}
		}
	case "executable":
		if info.IsDir() {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "path is a directory"}
		}
		if info.Mode()&0o111 == 0 {
			return appwire.PathValidateResponse{Path: path, Valid: false, Error: "path is not executable"}
		}
	default:
		return appwire.PathValidateResponse{Path: path, Valid: false, Error: "unknown path kind: " + kind}
	}
	return appwire.PathValidateResponse{Path: path, Valid: true}
}
