package main

import (
	"io/fs"
	"os"
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

// scanGoSources reads every non-test Go file under root that the build can
// compile. Worktrees, vendored trees and testdata are skipped: they hold other
// checkouts or fixtures, not consumers.
func scanGoSources(root string) ([]goSource, error) {
	var out []goSource
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, goSource{path: filepath.ToSlash(rel), text: string(text)})
		return nil
	})
	return out, err
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
