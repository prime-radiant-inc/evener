package fspaths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzSanitizeDirPrefix(f *testing.F) {
	for _, prefix := range []string{"", "/tmp/", "../etc", "/tmp/a/../b", "  /tmp/x  ", "/tmp/.", "/tmp/..", "/tmp/...", "/tmp/./", "."} {
		f.Add(prefix)
	}
	f.Fuzz(func(t *testing.T, prefix string) {
		if len(prefix) > 4096 {
			t.Skip()
		}
		got, err := SanitizeDirPrefix(prefix)
		if err != nil {
			if !strings.Contains(filepath.Clean(strings.TrimSpace(prefix)), "..") {
				t.Fatalf("unexpected sanitize error for %q: %v", prefix, err)
			}
			return
		}
		// The result is Clean apart from the two markers the autocomplete's
		// prefix protocol carries in the string itself: a trailing separator
		// ("list this directory's children") and a lone trailing dot ("filter
		// to the dotted names"). Strip those and the rest must be canonical.
		bare := strings.TrimSuffix(got, string(filepath.Separator))
		bare = strings.TrimSuffix(bare, string(filepath.Separator)+".")
		if strings.TrimSpace(prefix) != "" && bare != "" && filepath.Clean(bare) != bare {
			t.Fatalf("result is not clean: %q", got)
		}
		// A traversal element never survives sanitization: it is either
		// normalized away or rejected.
		for seg := range strings.SplitSeq(got, string(filepath.Separator)) {
			if seg == ".." {
				t.Fatalf("traversal survived sanitization: %q", got)
			}
		}
	})
}

func FuzzResolveInRoot(f *testing.F) {
	root, err := filepath.EvalSymlinks(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside"), []byte("x"), 0o600); err != nil {
		f.Fatal(err)
	}
	for _, rel := range []string{"inside", "missing", "../outside", "/etc/passwd", ".", " /000"} {
		f.Add(rel)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		if len(rel) > 4096 || strings.IndexByte(rel, 0) >= 0 {
			t.Skip()
		}
		got, err := ResolveInRoot(root, rel)
		if err == nil && !withinRoot(root, got) {
			t.Fatalf("resolved outside root: %q", got)
		}
		trimmedRel := strings.TrimSpace(rel)
		joined := filepath.Clean(trimmedRel)
		if !filepath.IsAbs(trimmedRel) {
			joined = filepath.Clean(filepath.Join(root, trimmedRel))
		}
		if errors.Is(err, ErrPathEscapesRoot) && withinRoot(root, joined) {
			// A lexical in-root path can still escape through a symlink. This fake
			// root contains no symlinks, so that result would violate containment.
			t.Fatalf("in-root path %q reported as escape", rel)
		}
	})
}
