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
