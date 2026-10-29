package main

import (
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

// scanGoSources reads every non-test Go file the repository tracks or would
// track (untracked, not ignored). Asking git keeps ignored trees (reference
// copies, review caches, nested checkouts) out of the inventory, so it does not
// depend on what a particular checkout happens to hold.
func scanGoSources(root string) ([]goSource, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go")
	listing, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing Go files in %s: %w", root, err)
	}
	var out []goSource
	for _, rel := range strings.Split(string(listing), "\x00") {
		if rel == "" || strings.HasSuffix(rel, "_test.go") || underTestdata(rel) {
			continue
		}
		text, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // tracked but deleted in this checkout
		}
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
