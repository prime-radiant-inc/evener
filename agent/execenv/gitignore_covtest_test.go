package execenv

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// TestIgnoreSet_Matches_NilSet covers the nil-set path (line 99-100).
func TestIgnoreSet_Matches_NilSet(t *testing.T) {
	t.Parallel()
	var s *ignoreSet
	if s.matches("foo.txt", false) {
		t.Fatal("expected false for nil set")
	}
	if s.matches("foo/", true) {
		t.Fatal("expected false for nil set (dir)")
	}
}

// TestIgnoreSet_Matches_DotDir covers matching with rel == "." (line 106-107).
func TestIgnoreSet_Matches_DotDir(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, ".", "*.tmp")
	if !set.matches("file.tmp", false) {
		t.Fatal("expected file.tmp to be ignored")
	}
	if set.matches("file.go", false) {
		t.Fatal("expected file.go NOT to be ignored")
	}
}

// TestIgnoreSet_Matches_PathEqualsDir covers relPath == id.rel (line 108-109).
func TestIgnoreSet_Matches_PathEqualsDir(t *testing.T) {
	t.Parallel()
	content := "*.log\n"
	fsys := fstest.MapFS{
		"sub":            {Mode: os.ModeDir | 0o755},
		"sub/.gitignore": {Data: []byte(content)},
	}
	set, err := loadIgnoreSet(fsys, nil, newGlobBudget("glob"))
	if err != nil {
		t.Fatal(err)
	}
	// "sub" == id.rel, so rel becomes "." — "*.log" won't match "."
	if set.matches("sub", false) {
		t.Fatal("expected sub (file) NOT to be ignored")
	}
	// sub/file.log should be ignored because rel becomes "file.log"
	if !set.matches("sub/file.log", false) {
		t.Fatal("expected sub/file.log to be ignored")
	}
}

// TestIgnoreSet_Matches_PrefixMatch covers the prefix match path (line 110-111).
func TestIgnoreSet_Matches_PrefixMatch(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, "sub", "build/")
	if !set.matches("sub/build/", true) {
		t.Fatal("expected sub/build/ to be ignored (dir)")
	}
	if !set.matches("sub/build/out.txt", false) {
		t.Fatal("expected sub/build/out.txt to be ignored")
	}
}

// TestIgnoreSet_Matches_DefaultContinue covers the default-continue path
// (line 112-113) where relPath doesn't match any dir prefix.
func TestIgnoreSet_Matches_DefaultContinue(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, "sub", "*.log")
	if set.matches("other/file.txt", false) {
		t.Fatal("expected other/file.txt NOT to be ignored")
	}
}

// TestIgnoreSet_Matches_IsDir covers the isDir trailing-slash append (line 115-116).
func TestIgnoreSet_Matches_IsDir(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, ".", "node_modules/")
	if !set.matches("node_modules", true) {
		t.Fatal("expected node_modules dir to be ignored")
	}
}

// TestIsDotPath covers the isDotPath function (lines 139-146).
func TestIsDotPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{"foo.txt", false},
		{".git", true},
		{"foo/bar.txt", false},
		{"foo/.hidden", true},
		{".claude/worktrees", true},
		{"./foo", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isDotPath(tc.path); got != tc.want {
			t.Errorf("isDotPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestLoadIgnoreSet_SkipFile covers the skip path for a file (line 62-63).
func TestLoadIgnoreSet_SkipFile(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		".gitignore":      {Data: []byte("*.tmp\n")},
		"skipme":          {Data: []byte("data\n")},
		"skipme/file.txt": {Data: []byte("data\n")},
	}
	set, err := loadIgnoreSet(fsys, func(relPath string) bool {
		return relPath == "skipme"
	}, newGlobBudget("glob"))
	if err != nil {
		t.Fatal(err)
	}
	// The .gitignore at root should still be loaded.
	if !set.matches("test.tmp", false) {
		t.Fatal("expected test.tmp to be ignored by root .gitignore")
	}
}

// TestGlobMatchIsDir covers the globMatchIsDir function (lines 130-133).
func TestGlobMatchIsDir(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"file.txt": {Data: []byte("hello")},
		"subdir":   {Data: []byte(""), Mode: os.ModeDir | 0o755},
	}
	if isDir, _ := globMatchIsDir(context.Background(), fsys, "file.txt"); isDir {
		t.Fatal("expected file.txt NOT to be a directory")
	}
	if isDir, _ := globMatchIsDir(context.Background(), fsys, "subdir"); !isDir {
		t.Fatal("expected subdir to be a directory")
	}
	if isDir, _ := globMatchIsDir(context.Background(), fsys, "nonexistent"); isDir {
		t.Fatal("expected nonexistent NOT to be a directory")
	}
}

// loadIgnoreSetFromLines builds an in-memory filesystem with a .gitignore
// in the given directory and returns the loaded ignoreSet.
func loadIgnoreSetFromLines(t *testing.T, dir string, lines ...string) *ignoreSet {
	t.Helper()
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	content := sb.String()
	path := ".gitignore"
	if dir != "." {
		path = dir + "/.gitignore"
	}
	fsys := fstest.MapFS{
		path: {Data: []byte(content)},
	}
	set, err := loadIgnoreSet(fsys, nil, newGlobBudget("glob"))
	if err != nil {
		t.Fatal(err)
	}
	return set
}
