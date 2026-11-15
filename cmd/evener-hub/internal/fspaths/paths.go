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
	"slices"
	"strings"
)

type pathOps struct {
	evalSymlinks func(string) (string, error)
	stat         func(string) (os.FileInfo, error)
}

var osPathOps = pathOps{
	evalSymlinks: filepath.EvalSymlinks,
	stat:         os.Stat,
}

// CanonicalizeDir resolves a user-supplied directory path to its canonical
// absolute form. The path must be absolute, must exist, and must be a
// directory. Symlinks are resolved.
//
// Today the hub only listens on loopback so this guards against accidents
// rather than attacks; the same code paths will get reused if the hub ever
// exposes beyond loopback, so canonicalize at the boundary and let callers
// trust the result.
func CanonicalizeDir(p string) (string, error) {
	return canonicalizeDir(p, osPathOps)
}

func canonicalizeDir(p string, ops pathOps) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path is empty")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	cleaned := filepath.Clean(p)
	resolved, err := ops.evalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve: %w", err)
	}
	info, err := ops.stat(resolved)
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
// existing path. A relative rel is joined onto root; an absolute rel is
// accepted when its canonical path lives inside the canonical root, including
// a spelling that reaches the same path through a symlinked ancestor.
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
		// An absolute request may name a path through a symlinked ancestor
		// (macOS spells /private/var/... as /var/...). Canonicalize the
		// existing parent and retry the lexical check, so that spelling is
		// accepted without an extra symlink walk on the normal spelling. A
		// retry that still is not lexically contained is not refused here:
		// the symlink-resolved check below makes the final call, so a request
		// that is exactly a symlinked spelling of the root, and a missing tail
		// whose canonical spelling would report the same miss, follow the same
		// rule as their canonical spellings.
		if !filepath.IsAbs(rel) {
			return "", ErrPathEscapesRoot
		}
		if realParent, err := filepath.EvalSymlinks(filepath.Dir(joined)); err == nil {
			joined = filepath.Join(realParent, filepath.Base(joined))
		}
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
// Two pieces of the autocomplete's prefix protocol live in the string itself
// and filepath.Clean erases both, so each is restored afterwards:
//
//   - A trailing separator distinguishes "list this directory's contents" from
//     "filter siblings by basename"; Clean drops trailing slashes.
//   - A final component of a single "." is the user asking to see the DOTTED
//     names (the autocomplete hides them until the filter starts with a dot);
//     Clean treats that dot as a no-op path element and removes it, which would
//     turn "/dir/." into a basename filter over /dir's parent and answer a
//     directory full of dotfiles with nothing.
//
// A bare "." needs no restoration: Clean leaves it alone. Every longer dot run
// ("..." and up) is an ordinary filename Clean already preserves, and ".." is a
// real path element that stays a traversal.
func SanitizeDirPrefix(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	sep := string(filepath.Separator)
	hadTrailingSlash := strings.HasSuffix(p, sep)
	hadDotFilter := strings.HasSuffix(p, sep+".")
	cleaned := filepath.Clean(p)
	if slices.Contains(strings.Split(cleaned, sep), "..") {
		return "", errors.New("path contains traversal")
	}
	switch {
	case hadDotFilter:
		if !strings.HasSuffix(cleaned, sep) {
			cleaned += sep
		}
		cleaned += "."
	case hadTrailingSlash && !strings.HasSuffix(cleaned, sep):
		cleaned += sep
	}
	return cleaned, nil
}
