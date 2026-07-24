package fspaths

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/envvars"
)

func CompletePaths(params appwire.PathsCompleteParams) (appwire.PathsCompleteResponse, error) {
	return completePaths(params, os.ReadDir)
}

func completePaths(params appwire.PathsCompleteParams, readDir func(string) ([]os.DirEntry, error)) (appwire.PathsCompleteResponse, error) {
	prefix := params.Prefix
	if prefix == "" {
		prefix = envvars.Home.Getenv()
	}
	if strings.HasPrefix(prefix, "~/") || prefix == "~" {
		prefix = filepath.Join(envvars.Home.Getenv(), strings.TrimPrefix(prefix, "~"))
	}
	cleaned, err := SanitizeDirPrefix(prefix)
	if err != nil {
		return appwire.PathsCompleteResponse{}, nil //nolint:nilerr // autocomplete: an unsanitizable prefix yields no suggestions, not an error
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

	entries, err := readDir(listDir)
	if err != nil {
		return appwire.PathsCompleteResponse{}, nil //nolint:nilerr // autocomplete: an unreadable directory yields no suggestions, not an error
	}
	type match struct {
		path  string
		score int
	}
	matches := make([]match, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(filter, ".") {
			continue
		}
		score, matched := directoryMatchScore(filter, name)
		if !matched {
			continue
		}
		matches = append(matches, match{path: filepath.Join(listDir, name), score: score})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return matches[i].path < matches[j].path
	})
	if params.Limit > 0 && len(matches) > params.Limit {
		matches = matches[:params.Limit]
	}
	results := make([]string, len(matches))
	for i := range matches {
		results[i] = matches[i].path
	}
	return appwire.PathsCompleteResponse{Data: results}, nil
}

func directoryMatchScore(query, name string) (int, bool) {
	query = strings.ToLower(query)
	name = strings.ToLower(name)
	if query == "" || name == query {
		return 0, true
	}
	if strings.HasPrefix(name, query) {
		return 1, true
	}
	if index := strings.Index(name, query); index >= 0 {
		return 10 + index, true
	}

	queryRunes := []rune(query)
	matched := 0
	first := -1
	for index, char := range []rune(name) {
		if char != queryRunes[matched] {
			continue
		}
		if first < 0 {
			first = index
		}
		matched++
		if matched == len(queryRunes) {
			gaps := index - first + 1 - len(queryRunes)
			return 100 + first + gaps, true
		}
	}
	return 0, false
}

func ValidateLaunchPath(params appwire.PathValidateParams) appwire.PathValidateResponse {
	path := strings.TrimSpace(params.Path)
	kind := strings.TrimSpace(params.Kind)
	if path == "" {
		return appwire.PathValidateResponse{Valid: false, Error: "path is required"}
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		path = filepath.Join(envvars.Home.Getenv(), strings.TrimPrefix(path, "~"))
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
