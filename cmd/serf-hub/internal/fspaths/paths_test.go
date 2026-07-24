package fspaths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

func checkCompletePaths_EmptyHomeUsesRoot(t *testing.T) {
	t.Setenv("HOME", "")
	var gotDir string
	resp, err := completePaths(appwire.PathsCompleteParams{}, func(dir string) ([]os.DirEntry, error) {
		gotDir = dir
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != string(filepath.Separator) {
		t.Fatalf("ReadDir called with %q, want filesystem root", gotDir)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("fake empty root returned %v", resp.Data)
	}
}

// fakeDirEntry stands in for a real directory entry: completePaths only reads
// Name and IsDir, so nothing else needs a real inode behind it.
type fakeDirEntry struct {
	name string
	dir  bool
}

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return e.dir }
func (e fakeDirEntry) Type() fs.FileMode          { return 0 }
func (e fakeDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }

func fakeReadDir(entries ...fakeDirEntry) func(string) ([]os.DirEntry, error) {
	return func(string) ([]os.DirEntry, error) {
		out := make([]os.DirEntry, len(entries))
		for i := range entries {
			out[i] = entries[i]
		}
		return out, nil
	}
}

// basePath names an entry inside the fake listed directory, "/base".
func basePath(name string) string {
	return filepath.Join(string(filepath.Separator), "base", name)
}

// baseDirPrefix is the prefix that lists /base's children (trailing separator).
func baseDirPrefix(filter string) string {
	return basePath("") + string(filepath.Separator) + filter
}

func checkCompletePaths_DirsOnlyExcludesFilesUnsuffixed(t *testing.T) {
	resp, err := completePaths(appwire.PathsCompleteParams{Prefix: baseDirPrefix("")}, fakeReadDir(
		fakeDirEntry{name: "sub", dir: true},
		fakeDirEntry{name: "file.txt"},
	))
	if err != nil {
		t.Fatal(err)
	}
	// Without IncludeFiles the response is byte-for-byte what it always was:
	// directories only, and no trailing separator for any caller to strip.
	want := []string{basePath("sub")}
	if !reflect.DeepEqual(resp.Data, want) {
		t.Fatalf("dirs-only data = %v, want %v", resp.Data, want)
	}
}

func checkCompletePaths_IncludeFilesReturnsBoth(t *testing.T) {
	resp, err := completePaths(appwire.PathsCompleteParams{Prefix: baseDirPrefix(""), IncludeFiles: true}, fakeReadDir(
		fakeDirEntry{name: "sub", dir: true},
		fakeDirEntry{name: "file.txt"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("includeFiles data = %v, want one directory and one file", resp.Data)
	}
}

func checkCompletePaths_IncludeFilesMarksDirsWithSeparator(t *testing.T) {
	resp, err := completePaths(appwire.PathsCompleteParams{Prefix: baseDirPrefix(""), IncludeFiles: true}, fakeReadDir(
		fakeDirEntry{name: "sub", dir: true},
		fakeDirEntry{name: "afile.txt"},
	))
	if err != nil {
		t.Fatal(err)
	}
	// Equal-scoring entries sort by path, so the file sorts ahead of the dir.
	want := []string{basePath("afile.txt"), basePath("sub") + string(filepath.Separator)}
	if !reflect.DeepEqual(resp.Data, want) {
		t.Fatalf("includeFiles data = %v, want %v", resp.Data, want)
	}
}

func checkCompletePaths_IncludeFilesHidesDotfilesUntilDotTyped(t *testing.T) {
	readDir := fakeReadDir(
		fakeDirEntry{name: ".env"},
		fakeDirEntry{name: "extra"},
	)
	dotEnv := basePath(".env")
	for _, filter := range []string{"", "e"} {
		prefix := baseDirPrefix(filter)
		resp, err := completePaths(appwire.PathsCompleteParams{Prefix: prefix, IncludeFiles: true}, readDir)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(resp.Data, dotEnv) {
			t.Fatalf("prefix %q returned %v, want no dotfile until a leading dot is typed", prefix, resp.Data)
		}
	}
	resp, err := completePaths(appwire.PathsCompleteParams{Prefix: baseDirPrefix(".e"), IncludeFiles: true}, readDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(resp.Data, dotEnv) {
		t.Fatalf("dot-filtered data = %v, want %s", resp.Data, dotEnv)
	}
}

func checkCompletePaths_IncludeFilesLimitCapsCombinedResult(t *testing.T) {
	// The files sort ahead of the directories, so a cap that only counted
	// directories would let all four through.
	resp, err := completePaths(appwire.PathsCompleteParams{Prefix: baseDirPrefix(""), IncludeFiles: true, Limit: 3}, fakeReadDir(
		fakeDirEntry{name: "zed1", dir: true},
		fakeDirEntry{name: "zed2", dir: true},
		fakeDirEntry{name: "apex1"},
		fakeDirEntry{name: "apex2"},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{basePath("apex1"), basePath("apex2"), basePath("zed1") + string(filepath.Separator)}
	if !reflect.DeepEqual(resp.Data, want) {
		t.Fatalf("limited data = %v, want %v", resp.Data, want)
	}
}

func checkCanonicalizeDir_StatErrorAfterResolution(t *testing.T) {
	want := errors.New("stat failed")
	_, err := canonicalizeDir(t.TempDir(), pathOps{
		evalSymlinks: filepath.EvalSymlinks,
		stat: func(string) (os.FileInfo, error) {
			return nil, want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("canonicalizeDir error = %v, want %v", err, want)
	}
}

func checkCanonicalizeDir_RejectsRelative(t *testing.T) {
	if _, err := CanonicalizeDir("foo/bar"); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func checkCanonicalizeDir_RejectsEmpty(t *testing.T) {
	if _, err := CanonicalizeDir(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func checkCanonicalizeDir_RejectsNonexistent(t *testing.T) {
	if _, err := CanonicalizeDir("/nonexistent/path/that/should/not/exist"); err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func checkCanonicalizeDir_RejectsFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalizeDir(f); err == nil {
		t.Fatal("expected error for file path")
	}
}

func checkCanonicalizeDir_ResolvesSymlink(t *testing.T) {
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

func checkCanonicalizeDir_NormalizesTraversal(t *testing.T) {
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

func checkSanitizeDirPrefix_PreservesTrailingSlash(t *testing.T) {
	got, err := SanitizeDirPrefix("/Users/jesse/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/") {
		t.Fatalf("expected trailing slash, got %q", got)
	}
}

func checkSanitizeDirPrefix_RejectsTraversal(t *testing.T) {
	if _, err := SanitizeDirPrefix("../etc/passwd"); err == nil {
		t.Fatal("expected error for traversal")
	}
}

func checkSanitizeDirPrefix_NormalizesInternalTraversal(t *testing.T) {
	got, err := SanitizeDirPrefix("/Users/jesse/foo/../bar")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/jesse/bar" {
		t.Fatalf("expected /Users/jesse/bar, got %q", got)
	}
}

func checkSanitizeDirPrefix_Empty(t *testing.T) {
	got, err := SanitizeDirPrefix("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func checkResolveInRoot_AllowsFileInsideRoot(t *testing.T) {
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

func checkResolveInRoot_AllowsNestedFile(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "deep.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveInRoot(root, "a/b/deep.txt")
	if err != nil {
		t.Fatalf("nested file should resolve: %v", err)
	}
	want, _ := filepath.EvalSymlinks(f)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func checkResolveInRoot_RejectsDotDotTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInRoot(root, "../"+filepath.Base(outside)); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected ErrPathEscapesRoot, got %v", err)
	}
}

func checkResolveInRoot_RejectsAbsoluteOutside(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveInRoot(root, "/etc/passwd"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected ErrPathEscapesRoot, got %v", err)
	}
}

func checkResolveInRoot_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ResolveInRoot(root, "escape"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected ErrPathEscapesRoot for symlink escape, got %v", err)
	}
}

func checkResolveInRoot_MissingFileNotEscape(t *testing.T) {
	root := t.TempDir()
	// A path inside the root that doesn't exist returns a non-escape error so
	// the caller renders 404, not 403.
	_, err := ResolveInRoot(root, "nope.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if errors.Is(err, ErrPathEscapesRoot) {
		t.Fatal("missing in-root file should not be an escape error")
	}
}

func checkResolveInRoot_RejectsEmpty(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveInRoot(root, ""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := ResolveInRoot("", "x"); err == nil {
		t.Fatal("expected error for empty root")
	}
}

func checkResolveInRoot_RejectsMissingRoot(t *testing.T) {
	if _, err := ResolveInRoot(filepath.Join(t.TempDir(), "missing"), "file"); err == nil {
		t.Fatal("expected error for missing root")
	}
}
