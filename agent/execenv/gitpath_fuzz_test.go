package execenv

import (
	"path/filepath"
	"testing"
)

// FuzzMainRootFromGitdirPointer drives the pure pointer-parsing core. It must
// never panic, and whenever it reports a main root, that root must be exactly
// the ".git/worktrees/<id>" tail stripped from the resolved pointer path.
func FuzzMainRootFromGitdirPointer(f *testing.F) {
	seeds := []struct{ content, ancestor string }{
		{"gitdir: /main/.git/worktrees/wt\n", "/main/wt"},
		{"gitdir: ../.git/worktrees/wt", "/repo/wt"},
		{"gitdir: ../../.git/worktrees/x\n", "/a/b/c"},
		{"gitdir: ../.git/modules/sub", "/super/sub"},
		{"gitdir: /abs/.git/modules/sub\n", "/x"},
		{"gitdir:\n", "/x"},
		{"gitdir: relative/worktrees/id", "/base"},
		{"gitdir: \x00\x00/worktrees/id", "/base"},
		{"garbage with no pointer", "/x"},
		{"", ""},
	}
	for _, s := range seeds {
		f.Add(s.content, s.ancestor)
	}

	f.Fuzz(func(t *testing.T, content, ancestor string) {
		root, ok := mainRootFromGitdirPointer(content, ancestor)
		if !ok {
			return
		}
		if root == "" || root == "." {
			t.Fatalf("ok but empty root: content=%q ancestor=%q", content, ancestor)
		}
		gitdir, parsed := parseGitdirPointer(content)
		if !parsed {
			t.Fatalf("ok without a parseable gitdir: content=%q", content)
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(ancestor, gitdir)
		}
		gitdir = filepath.Clean(gitdir)
		if base := filepath.Base(filepath.Dir(gitdir)); base != "worktrees" {
			t.Fatalf("ok but pointer parent is %q, not worktrees: %q", base, gitdir)
		}
		if want := filepath.Dir(filepath.Dir(filepath.Dir(gitdir))); root != want {
			t.Fatalf("root = %q, want stripped %q", root, want)
		}
	})
}
