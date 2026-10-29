package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// goSource is one non-test Go file: where it lives and what it says.
type goSource struct {
	path string // slash-separated, relative to the repo root
	text string
}

func (s goSource) dir() string { return filepath.ToSlash(filepath.Dir(s.path)) }

// repoFiles lists the files git tracks or would track (untracked, not
// ignored) under the given pathspecs, as slash paths relative to root. Asking
// git keeps ignored trees (reference copies, review caches, nested checkouts)
// out of the inventory, so it does not depend on what a particular checkout
// happens to hold. Test files, testdata and files deleted from this checkout
// are left out.
func repoFiles(root string, pathspecs ...string) ([]string, error) {
	args := append([]string{"-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--"}, pathspecs...)
	listing, err := exec.CommandContext(context.Background(), "git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("listing %v in %s: %w", pathspecs, root, err)
	}
	var out []string
	for rel := range strings.SplitSeq(string(listing), "\x00") {
		if rel == "" || strings.HasSuffix(rel, "_test.go") || underTestdata(rel) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

// scanGoSources reads every non-test Go file in the repository.
func scanGoSources(root string) ([]goSource, error) {
	paths, err := repoFiles(root, "*.go")
	if err != nil {
		return nil, err
	}
	out := make([]goSource, 0, len(paths))
	for _, rel := range paths {
		text, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		out = append(out, goSource{path: rel, text: string(text)})
	}
	return out, nil
}

// underTestdata reports whether rel is inside a testdata directory, whose Go
// files the build never compiles.
func underTestdata(rel string) bool {
	return strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/")
}

// docDirs are the reference docs a knob's doc row can live in: the top-level
// guides and the developer docs. The dated design records under docs/superpowers
// and docs/design are history, not the row a reader should land on.
var docDirs = []string{"docs", "docs/developing-evener"}

// preferredDocs are checked first, so a knob several guides mention links to the
// one that owns it.
var preferredDocs = []string{
	"docs/developing-evener/environment.md",
	"docs/environment-variables.md",
	"docs/configuration.md",
	"docs/evener-hub.md",
}

// docMention returns the first reference doc (path relative to docs/) whose
// text satisfies mentions, or "".
func docMention(root string, mentions func(text string) bool) string {
	candidates := append([]string(nil), preferredDocs...)
	for _, dir := range docDirs {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "config-knobs.md" {
				candidates = append(candidates, dir+"/"+e.Name())
			}
		}
	}
	for _, rel := range candidates {
		if text, err := os.ReadFile(filepath.Join(root, rel)); err == nil && mentions(string(text)) {
			return strings.TrimPrefix(rel, "docs/")
		}
	}
	return ""
}

// addTo records dir under key in a set-valued index.
func addTo(index map[string]map[string]bool, key, dir string) {
	if index[key] == nil {
		index[key] = map[string]bool{}
	}
	index[key][dir] = true
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
