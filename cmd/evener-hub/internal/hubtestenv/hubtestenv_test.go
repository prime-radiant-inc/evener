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
	sep := string(os.PathSeparator)
	type containmentCase struct {
		name, root, path string
		want             bool
	}
	check := func(t *testing.T, cases []containmentCase) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := ContainedIn(tc.root, tc.path); got != tc.want {
					t.Errorf("ContainedIn(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
				}
			})
		}
	}
	check(t, []containmentCase{
		{"descendant nothing has created yet", realRoot, filepath.Join(realRoot, "config", "evener", "AGENTS.md"), true},
		{"dot-dot traversal", realRoot, realRoot + sep + ".." + sep + "outside" + sep + "x", false},
		{"the root itself", realRoot, realRoot, false},
		{"sibling sharing the root as a prefix", realRoot, realRoot + "-sibling", false},
		{"relative path", realRoot, "config" + sep + "evener", false},
		{"empty path", realRoot, "", false},
	})
	t.Run("through symlinks", func(t *testing.T) {
		link := filepath.Join(base, "link")
		escape := filepath.Join(realRoot, "escape")
		dangling := filepath.Join(realRoot, "dangling")
		for _, ln := range []struct{ path, target string }{
			{link, realRoot},
			{escape, outside},
			{dangling, filepath.Join(outside, "not-yet-created")},
		} {
			if err := os.Symlink(ln.target, ln.path); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
		}
		check(t, []containmentCase{
			{"root reached through a symlink", link, filepath.Join(realRoot, "home"), true},
			{"path reached through a symlink", realRoot, filepath.Join(link, "home"), true},
			{"symlink planted under the root", realRoot, filepath.Join(escape, "AGENTS.md"), false},
			{"dangling symlink planted under the root", realRoot, filepath.Join(dangling, "AGENTS.md"), false},
		})
	})
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
	env := &Env{Root: root}
	roots := []NamedPath{{Name: "state glob", Path: filepath.Join(projects, "*")}}
	if got := env.PathsOutside(roots); len(got) != 0 {
		t.Fatalf("PathsOutside with no matches = %q, want none", got)
	}
	t.Run("escaping match", func(t *testing.T) {
		escape := filepath.Join(projects, "escape")
		if err := os.Symlink(outside, escape); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Remove(escape); err != nil {
				t.Error(err)
			}
		})
		got := env.PathsOutside(roots)
		if len(got) != 1 || !strings.Contains(got[0], escape) {
			t.Fatalf("PathsOutside with an escaping match = %q, want exactly that match reported", got)
		}
	})
}
