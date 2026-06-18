// Package fspaths holds the boundary-hardening filesystem helpers that turn
// user-supplied path strings into trusted absolute paths: directory
// canonicalization, dir-prefix sanitization, directory autocomplete, and
// launch command/file/dir validation against the appwire path-validate
// contract.
package fspaths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalizeDir resolves a user-supplied directory path to its canonical
// absolute form. The path must be absolute, must exist, and must be a
// directory. Symlinks are resolved.
//
// Today the hub only listens on loopback so this guards against accidents
// rather than attacks; the same code paths will get reused if the hub ever
// exposes beyond loopback, so canonicalize at the boundary and let callers
// trust the result.
func CanonicalizeDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path is empty")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	cleaned := filepath.Clean(p)
	// After Clean on an absolute path, ".." segments are normally collapsed
	// (Clean("/../foo") == "/foo"). A residual ".." would be a parser bug;
	// reject defensively.
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return "", errors.New("path contains traversal")
		}
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return resolved, nil
}

// ErrPathEscapesRoot is returned by ResolveInRoot when the requested path
// resolves to a location outside the root directory. Callers should treat it
// as a forbidden access rather than a missing file.
var ErrPathEscapesRoot = errors.New("path escapes root")

// ResolveInRoot resolves a (possibly relative) request path against a trusted
// root directory and returns the absolute path ONLY if it stays inside the
// root after both the path and the root are symlink-resolved. This is the
// boundary check for serving repo files into a read-only document pane.
//
// Defense layers, each independently sufficient:
//   - The cleaned join is rejected if it doesn't have the cleaned root as a
//     prefix (catches "../" traversal and absolute-path escapes).
//   - The symlink-resolved target is rejected if it isn't contained by the
//     symlink-resolved root (catches a symlink inside root pointing outside).
//
// The root itself must be an existing directory; rel must resolve to an
// existing path. A relative rel is joined onto root; an absolute rel is only
// accepted if it already lives inside root.
func ResolveInRoot(root, rel string) (string, error) {
	root = strings.TrimSpace(root)
	rel = strings.TrimSpace(rel)
	if root == "" || rel == "" {
		return "", errors.New("empty root or path")
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", err
	}

	var joined string
	if filepath.IsAbs(rel) {
		joined = filepath.Clean(rel)
	} else {
		joined = filepath.Clean(filepath.Join(realRoot, rel))
	}
	// Lexical containment: the cleaned target must sit under the cleaned root.
	if !withinRoot(realRoot, joined) {
		return "", ErrPathEscapesRoot
	}

	// Symlink-resolved containment: resolve the target and re-check, so a
	// symlink inside root that points elsewhere can't smuggle the access out.
	realTarget, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	if !withinRoot(realRoot, realTarget) {
		return "", ErrPathEscapesRoot
	}
	return realTarget, nil
}

// withinRoot reports whether target is root itself or a descendant of root.
// Both arguments must already be cleaned absolute paths.
func withinRoot(root, target string) bool {
	if target == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}

// SanitizeDirPrefix cleans a directory-prefix string used by the autocomplete
// endpoint. The prefix may not exist (the user is typing) so we don't
// EvalSymlinks; we only normalize separators and reject traversal.
//
// Trailing-slash semantics are preserved: filepath.Clean drops trailing
// slashes, but the autocomplete uses the trailing slash to distinguish
// "list contents of dir" from "filter siblings by basename".
func SanitizeDirPrefix(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	hadTrailingSlash := strings.HasSuffix(p, string(filepath.Separator))
	cleaned := filepath.Clean(p)
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return "", errors.New("path contains traversal")
		}
	}
	if hadTrailingSlash && !strings.HasSuffix(cleaned, string(filepath.Separator)) {
		cleaned += string(filepath.Separator)
	}
	return cleaned, nil
}
