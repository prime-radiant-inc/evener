package fspaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalizeDir_RejectsRelative(t *testing.T) {
	if _, err := CanonicalizeDir("foo/bar"); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestCanonicalizeDir_RejectsEmpty(t *testing.T) {
	if _, err := CanonicalizeDir(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestCanonicalizeDir_RejectsNonexistent(t *testing.T) {
	if _, err := CanonicalizeDir("/nonexistent/path/that/should/not/exist"); err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestCanonicalizeDir_RejectsFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalizeDir(f); err == nil {
		t.Fatal("expected error for file path")
	}
}

func TestCanonicalizeDir_ResolvesSymlink(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := CanonicalizeDir(link)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.EvalSymlinks(realDir)
	if resolved != expected {
		t.Fatalf("expected %s, got %s", expected, resolved)
	}
}

func TestCanonicalizeDir_NormalizesTraversal(t *testing.T) {
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// /tmp/.../sub/../sub -> /tmp/.../sub
	resolved, err := CanonicalizeDir(sub + "/../sub")
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.EvalSymlinks(sub)
	if resolved != expected {
		t.Fatalf("expected %s, got %s", expected, resolved)
	}
}

func TestSanitizeDirPrefix_PreservesTrailingSlash(t *testing.T) {
	got, err := SanitizeDirPrefix("/Users/jesse/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/") {
		t.Fatalf("expected trailing slash, got %q", got)
	}
}

func TestSanitizeDirPrefix_RejectsTraversal(t *testing.T) {
	if _, err := SanitizeDirPrefix("../etc/passwd"); err == nil {
		t.Fatal("expected error for traversal")
	}
}

func TestSanitizeDirPrefix_NormalizesInternalTraversal(t *testing.T) {
	got, err := SanitizeDirPrefix("/Users/jesse/foo/../bar")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/jesse/bar" {
		t.Fatalf("expected /Users/jesse/bar, got %q", got)
	}
}

func TestSanitizeDirPrefix_Empty(t *testing.T) {
	got, err := SanitizeDirPrefix("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestResolveInRoot_AllowsFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveInRoot(root, "ok.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, _ := filepath.EvalSymlinks(f)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveInRoot_AllowsNestedFile(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "deep.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInRoot(root, "a/b/deep.txt"); err != nil {
		t.Fatalf("nested file should resolve: %v", err)
	}
}

func TestResolveInRoot_RejectsDotDotTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInRoot(root, "../"+filepath.Base(outside)); err != ErrPathEscapesRoot {
		t.Fatalf("expected ErrPathEscapesRoot, got %v", err)
	}
}

func TestResolveInRoot_RejectsAbsoluteOutside(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveInRoot(root, "/etc/passwd"); err != ErrPathEscapesRoot {
		t.Fatalf("expected ErrPathEscapesRoot, got %v", err)
	}
}

func TestResolveInRoot_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ResolveInRoot(root, "escape"); err != ErrPathEscapesRoot {
		t.Fatalf("expected ErrPathEscapesRoot for symlink escape, got %v", err)
	}
}

func TestResolveInRoot_MissingFileNotEscape(t *testing.T) {
	root := t.TempDir()
	// A path inside the root that doesn't exist returns a non-escape error so
	// the caller renders 404, not 403.
	_, err := ResolveInRoot(root, "nope.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if err == ErrPathEscapesRoot {
		t.Fatal("missing in-root file should not be an escape error")
	}
}

func TestResolveInRoot_RejectsEmpty(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveInRoot(root, ""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := ResolveInRoot("", "x"); err == nil {
		t.Fatal("expected error for empty root")
	}
}
