package hubtestenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContainedInResolvesSymlinksAndTraversal pins the containment test every
// package's guard relies on: it must see through a symlinked root (a macOS temp
// root lives under /var, which is /private/var), refuse a link planted under
// the root that points outside it, refuse dot-dot traversal, and still accept
// a default nothing has created yet.
func TestContainedInResolvesSymlinksAndTraversal(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{realRoot, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(realRoot, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(realRoot, "dangling")
	if err := os.Symlink(filepath.Join(outside, "not-yet-created"), dangling); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathSeparator)
	cases := []struct {
		name, root, path string
		want             bool
	}{
		{"descendant nothing has created yet", realRoot, filepath.Join(realRoot, "config", "evener", "AGENTS.md"), true},
		{"root reached through a symlink", link, filepath.Join(realRoot, "home"), true},
		{"path reached through a symlink", realRoot, filepath.Join(link, "home"), true},
		{"symlink planted under the root", realRoot, filepath.Join(escape, "AGENTS.md"), false},
		{"dangling symlink planted under the root", realRoot, filepath.Join(dangling, "AGENTS.md"), false},
		{"dot-dot traversal", realRoot, realRoot + sep + ".." + sep + "outside" + sep + "x", false},
		{"the root itself", realRoot, realRoot, false},
		{"sibling sharing the root as a prefix", realRoot, realRoot + "-sibling", false},
		{"relative path", realRoot, "config" + sep + "evener", false},
		{"empty path", realRoot, "", false},
	}
	for _, tc := range cases {
		if got := ContainedIn(tc.root, tc.path); got != tc.want {
			t.Errorf("%s: ContainedIn(%q, %q) = %v, want %v", tc.name, tc.root, tc.path, got, tc.want)
		}
	}
}

// TestRootsOutsideExpandsGlobs pins that a glob among the paths handed to
// PathsOutside is judged by what it matches, not by its literal prefix: a
// symlinked project directory planted under the state root and pointing outside
// it is reported, and a pattern with no matches is judged on its own path.
func TestRootsOutsideExpandsGlobs(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	projects := filepath.Join(root, "state", "evener", "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(projects, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	env := &Env{Root: root}
	roots := []NamedPath{{Name: "state glob", Path: filepath.Join(projects, "*")}}
	got := env.PathsOutside(roots)
	if len(got) != 1 || !strings.Contains(got[0], escape) {
		t.Fatalf("PathsOutside with an escaping match = %q, want exactly that match reported", got)
	}
	if err := os.Remove(escape); err != nil {
		t.Fatal(err)
	}
	if got := env.PathsOutside(roots); len(got) != 0 {
		t.Fatalf("PathsOutside with no matches = %q, want none", got)
	}
}
