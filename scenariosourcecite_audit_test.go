package serf_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scenarioGoCitation matches a card pointing at a place inside a Go file: a
// backticked path carrying an anchor, either the `#symbol` this convention
// prefers or the `:line` hint it replaces.
//
// The anchor is what makes it a citation. A backticked path on its own is a
// mention — "relocated from the deleted `security.go`", "the package was
// extracted out of `cmd/serf-tui/pending.go`" — and naming a file that is gone
// is the whole point of those sentences, so they are not checked.
var scenarioGoCitation = regexp.MustCompile("`([A-Za-z0-9._/-]+\\.go)(?:#([A-Za-z0-9_.]+)|:([0-9][0-9,-]*))`")

// TestScenarioSourceCitationsResolve keeps a card's pointer into Go code
// attached to a file that still exists. Kata 2mzk's audit deliberately exempts
// source paths from the no-line-numbers rule, because code has no headings to
// anchor to; that exemption said nothing about whether the paths were right.
// Kata ypwb sampled four and found three stale, and a whole-file rename or
// package move breaks every citation into it at once while every existing
// audit stays green.
//
// Cards abbreviate a path down to the part that reads well
// (`internal/hubcore/tree.go` for `cmd/serf-hub/internal/hubcore/tree.go`), so
// resolution is by path SUFFIX against the tree. That still catches the failure
// that matters — the file moved out from under the citation, or was renamed,
// or never existed under that name.
func TestScenarioSourceCitationsResolve(t *testing.T) {
	byBase := scenarioGoFilesByBase(t)
	var findings []string
	resolved := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range scenarioGoCitation.FindAllStringSubmatch(line, -1) {
				cited := m[1]
				if len(scenarioResolveGoPath(byBase, cited)) == 0 {
					findings = append(findings, path+":"+strconv.Itoa(i+1)+
						": `"+cited+"` names no Go file in the tree: "+strings.TrimSpace(line))
					continue
				}
				resolved++
			}
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. Cards cite Go code by the hundred today, so zero resolutions means
	// the citation needle broke, not that the citations left.
	if resolved == 0 {
		t.Fatalf("the Go-citation needle matched nothing across the corpus — " +
			"the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card that points into a Go file must name a file "+
			"that is still there — a package move or a rename silently turns "+
			"every citation into it into a pointer at nothing, and no other "+
			"audit sees it (kata ypwb). Repoint at the file that carries the "+
			"code now, or drop the anchor if the sentence is deliberately "+
			"naming something deleted:\n%s", strings.Join(findings, "\n"))
	}
}

// scenarioGoFilesByBase indexes every Go file in the worktree by base name, so
// a cited path suffix can be resolved without rewalking the tree per citation.
func scenarioGoFilesByBase(t *testing.T) map[string][]string {
	t.Helper()
	byBase := map[string][]string{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".go" {
			base := d.Name()
			byBase[base] = append(byBase[base], filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the worktree for Go files: %v", err)
	}
	return byBase
}

// scenarioResolveGoPath returns every Go file whose path is, or ends with, the
// cited path. Cards abbreviate, so `internal/hubcore/tree.go` must find
// `cmd/serf-hub/internal/hubcore/tree.go`; a bare `main.go` legitimately finds
// many, which is imprecise but not stale.
func scenarioResolveGoPath(byBase map[string][]string, cited string) []string {
	var out []string
	for _, candidate := range byBase[scenarioCitedBaseName(cited)] {
		if candidate == cited || strings.HasSuffix(candidate, "/"+cited) {
			out = append(out, candidate)
		}
	}
	return out
}

// scenarioCitedBaseName is the file name at the end of a citation path.
func scenarioCitedBaseName(cited string) string {
	if i := strings.LastIndexByte(cited, '/'); i >= 0 {
		return cited[i+1:]
	}
	return cited
}
