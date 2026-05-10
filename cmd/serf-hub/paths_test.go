package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalizeDir_RejectsRelative(t *testing.T) {
	if _, err := canonicalizeDir("foo/bar"); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestCanonicalizeDir_RejectsEmpty(t *testing.T) {
	if _, err := canonicalizeDir(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestCanonicalizeDir_RejectsNonexistent(t *testing.T) {
	if _, err := canonicalizeDir("/nonexistent/path/that/should/not/exist"); err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestCanonicalizeDir_RejectsFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalizeDir(f); err == nil {
		t.Fatal("expected error for file path")
	}
}

func TestCanonicalizeDir_ResolvesSymlink(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := canonicalizeDir(link)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.EvalSymlinks(real)
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
	resolved, err := canonicalizeDir(sub + "/../sub")
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.EvalSymlinks(sub)
	if resolved != expected {
		t.Fatalf("expected %s, got %s", expected, resolved)
	}
}

func TestSanitizeDirPrefix_PreservesTrailingSlash(t *testing.T) {
	got, err := sanitizeDirPrefix("/Users/jesse/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/") {
		t.Fatalf("expected trailing slash, got %q", got)
	}
}

func TestSanitizeDirPrefix_RejectsTraversal(t *testing.T) {
	if _, err := sanitizeDirPrefix("../etc/passwd"); err == nil {
		t.Fatal("expected error for traversal")
	}
}

func TestSanitizeDirPrefix_NormalizesInternalTraversal(t *testing.T) {
	got, err := sanitizeDirPrefix("/Users/jesse/foo/../bar")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/jesse/bar" {
		t.Fatalf("expected /Users/jesse/bar, got %q", got)
	}
}

func TestSanitizeDirPrefix_Empty(t *testing.T) {
	got, err := sanitizeDirPrefix("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
