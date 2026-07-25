package fspaths_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
)

func checkCompletePaths(t *testing.T) {
	// Use a fake home so that empty prefix and ~ expansion are deterministic.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a predictable directory structure inside home.
	dirs := []string{"Alpha", "Beta", "Gamma", ".hidden", "delta"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Add a regular file to verify the IsDir() filter in CompletePaths excludes files.
	if err := os.WriteFile(filepath.Join(home, "afile.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("empty prefix lists home's children", func(t *testing.T) {
		// The client sends an empty prefix for an empty field and the hub
		// resolves it to HOME's CONTENTS (spec §3.4) - not to HOME's siblings,
		// which is what a prefix of HOME with no trailing separator would mean.
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{
			filepath.Join(home, "Alpha"),
			filepath.Join(home, "Beta"),
			filepath.Join(home, "Gamma"),
			filepath.Join(home, "delta"),
		}
		if !reflect.DeepEqual(resp.Data, want) {
			t.Fatalf("empty-prefix data = %v, want %v", resp.Data, want)
		}
	})

	t.Run("prefix with ~/ expands to home", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: "~/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// filepath.Join(home, "/") strips the trailing slash, so "~/" resolves to
		// home itself (no trailing separator): the SUT lists home's parent
		// filtered by home's basename, yielding home. Pinning the exact result
		// catches a regression mapping ~ to some other directory (e.g. "/").
		want := []string{home}
		if len(resp.Data) != 1 || resp.Data[0] != home {
			t.Fatalf("expected %v after ~ expansion, got %v", want, resp.Data)
		}
	})

	t.Run("prefix ending with slash lists that dir", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: home + "/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Must contain all non-hidden dirs.
		var got []string
		for _, p := range resp.Data {
			got = append(got, filepath.Base(p))
		}
		want := []string{"Alpha", "Beta", "Gamma", "delta"}
		for _, w := range want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected %q in results, got %v", w, got)
			}
		}
		// Regular files must be excluded (exercises the IsDir() filter).
		for _, p := range resp.Data {
			if filepath.Base(p) == "afile.txt" {
				t.Fatalf("regular file afile.txt should be excluded from directory completions, got %v", resp.Data)
			}
		}
	})

	t.Run("prefix without trailing slash lists parent and filters", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: filepath.Join(home, "Al")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 1 || filepath.Base(resp.Data[0]) != "Alpha" {
			t.Fatalf("expected [Alpha], got %v", resp.Data)
		}
	})

	t.Run("unreadable dir returns empty response", func(t *testing.T) {
		// In external-test mode we may not be root; a non-existent dir
		// behaves the same as an unreadable one (ReadDir returns an error).
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: "/nonexistent-dir-12345/xyz"})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if len(resp.Data) != 0 {
			t.Fatalf("expected empty response for unreadable dir, got %v", resp.Data)
		}
	})

	t.Run("default returns all directories and explicit limit is honored", func(t *testing.T) {
		bigHome := t.TempDir()
		t.Setenv("HOME", bigHome)
		for i := 0; i < 40; i++ {
			if err := os.MkdirAll(filepath.Join(bigHome, fmt.Sprintf("dir%02d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: bigHome + "/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 40 {
			t.Fatalf("expected all 40 entries, got %d", len(resp.Data))
		}

		limited, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: bigHome + "/", Limit: 7})
		if err != nil {
			t.Fatalf("unexpected limited error: %v", err)
		}
		if len(limited.Data) != 7 {
			t.Fatalf("expected explicit limit of 7 entries, got %d", len(limited.Data))
		}
	})

	t.Run("final component fuzzy matches", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: filepath.Join(home, "lph")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 1 || filepath.Base(resp.Data[0]) != "Alpha" {
			t.Fatalf("expected fuzzy match [Alpha], got %v", resp.Data)
		}
	})

	t.Run("hidden dirs filtered when no filter", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: home + "/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, p := range resp.Data {
			if strings.HasPrefix(filepath.Base(p), ".") {
				t.Fatalf("hidden dir %q should be filtered", p)
			}
			if filepath.Base(p) == "afile.txt" {
				t.Fatalf("regular file %q should be excluded from directory completions", p)
			}
		}
	})

	t.Run("hidden dirs included when filter matches", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: filepath.Join(home, ".hid")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 1 || filepath.Base(resp.Data[0]) != ".hidden" {
			t.Fatalf("expected [.hidden], got %v", resp.Data)
		}
	})

	t.Run("case-insensitive filter matching", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: filepath.Join(home, "aL")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 1 || filepath.Base(resp.Data[0]) != "Alpha" {
			t.Fatalf("expected [Alpha], got %v", resp.Data)
		}
	})

	t.Run("results are sorted", func(t *testing.T) {
		resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: home + "/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) < 2 {
			t.Skip("not enough entries to verify sorting")
		}
		if !sort.StringsAreSorted(resp.Data) {
			t.Fatalf("expected sorted results, got %v", resp.Data)
		}
	})
}

// checkCompletePaths_SymlinksOnRealTree exercises symlink handling against a
// real directory tree rather than a hand-built entry list: os.ReadDir reports
// a symlink-to-directory with IsDir() false, which no fabricated DirEntry
// reproduces faithfully, and browsing to a symlinked project directory is the
// user-visible behavior at stake.
func checkCompletePaths_SymlinksOnRealTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	realDir := filepath.Join(home, "alpha")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(home, "beta.txt")
	if err := os.WriteFile(realFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"link-to-dir":  realDir,
		"link-to-file": realFile,
		"link-broken":  filepath.Join(home, "does-not-exist"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(home, name)); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
	}

	sep := string(filepath.Separator)
	cases := []struct {
		name         string
		includeFiles bool
		want         []string
	}{
		{
			// A symlink to a directory is a directory to browse into; a symlink
			// to a file and a broken symlink are not directories, so a
			// dirs-only field omits them.
			name: "dirs only lists the symlinked directory",
			want: []string{"alpha", "link-to-dir"},
		},
		{
			// With files in the mix every entry is listed, and the trailing
			// separator marks the two that are descendable. A broken symlink
			// cannot be resolved, so it goes out unsuffixed.
			name:         "includeFiles suffixes only the resolvable directories",
			includeFiles: true,
			want:         []string{"alpha" + sep, "beta.txt", "link-broken", "link-to-dir" + sep, "link-to-file"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: home + sep, IncludeFiles: tc.includeFiles})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := make([]string, len(tc.want))
			for i, name := range tc.want {
				want[i] = filepath.Join(home, strings.TrimSuffix(name, sep))
				if strings.HasSuffix(name, sep) {
					want[i] += sep
				}
			}
			if !reflect.DeepEqual(resp.Data, want) {
				t.Fatalf("completions = %v, want %v", resp.Data, want)
			}
		})
	}
}

func checkCompletePaths_TraversalReturnsNoSuggestions(t *testing.T) {
	resp, err := fspaths.CompletePaths(appwire.PathsCompleteParams{Prefix: "../"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("traversal returned suggestions: %v", resp.Data)
	}
}

// checkCompletePaths_EmptyResultMarshalsAsEmptyArray pins the WIRE shape of an
// empty completion, not just its length: a nil Data slice marshals as JSON
// `null`, and the generated TypeScript declares `data: string[]`, so a null
// reaches the browser as a value the type system promised could not exist and
// the first `.length` on it takes down the React tree.
func checkCompletePaths_EmptyResultMarshalsAsEmptyArray(t *testing.T) {
	empty := t.TempDir()
	cases := map[string]appwire.PathsCompleteParams{
		"unsanitizable prefix": {Prefix: "../"},
		"unreadable dir":       {Prefix: "/nonexistent-dir-12345/xyz"},
		"empty directory":      {Prefix: empty + "/"},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := fspaths.CompletePaths(params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			encoded, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != `{"data":[]}` {
				t.Fatalf("marshalled empty completion = %s, want {\"data\":[]}", encoded)
			}
		})
	}
}

func checkValidateLaunchPath(t *testing.T) {
	// Set up a temp directory with various files/dirs for testing.
	tmp := t.TempDir()

	// Create directories and files.
	regularDir := filepath.Join(tmp, "regularDir")
	if err := os.MkdirAll(regularDir, 0o755); err != nil {
		t.Fatal(err)
	}

	readOnlyDir := filepath.Join(tmp, "readOnlyDir")
	if err := os.MkdirAll(readOnlyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(readOnlyDir, 0o755) // restore for cleanup
	})

	executableFile := filepath.Join(tmp, "executable")
	if err := os.WriteFile(executableFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	nonExecutableFile := filepath.Join(tmp, "nonExecutable")
	if err := os.WriteFile(nonExecutableFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	regularFile := filepath.Join(tmp, "regularFile")
	if err := os.WriteFile(regularFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set a fake home for ~ expansion tests.
	t.Setenv("HOME", tmp)

	t.Run("empty path", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: "", Kind: "any"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is required" {
			t.Fatalf("expected 'path is required', got %q", resp.Error)
		}
	})

	t.Run("tilde prefix expansion", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: "~/regularFile", Kind: "file"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
		want := filepath.Join(tmp, "regularFile")
		if resp.Path != want {
			t.Fatalf("expected path %q, got %q", want, resp.Path)
		}
	})

	t.Run("command kind on PATH", func(t *testing.T) {
		// Use a known command that exists on PATH.
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: "sh", Kind: "command"})
		if !resp.Valid {
			t.Fatalf("expected valid for 'sh', got error: %s", resp.Error)
		}
		if !filepath.IsAbs(resp.Path) {
			t.Fatalf("expected absolute resolved path, got %q", resp.Path)
		}
		info, err := os.Stat(resp.Path)
		if err != nil {
			t.Fatalf("resolved path %q does not exist: %v", resp.Path, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("resolved path %q is not executable", resp.Path)
		}
	})

	t.Run("command kind not on PATH", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: "definitely-not-a-real-command-12345", Kind: "command"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error == "" {
			t.Fatal("expected error message")
		}
	})

	t.Run("non-absolute path", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: "relative/path", Kind: "file"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "absolute path required" {
			t.Fatalf("expected 'absolute path required', got %q", resp.Error)
		}
	})

	t.Run("output-file kind path is directory", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularDir, Kind: "output-file"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is a directory" {
			t.Fatalf("expected 'path is a directory', got %q", resp.Error)
		}
	})

	t.Run("output-file kind parent not writable", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: filepath.Join(readOnlyDir, "out.txt"), Kind: "output-file"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "parent directory is not writable" {
			t.Fatalf("expected 'parent directory is not writable', got %q", resp.Error)
		}
	})

	t.Run("output-file kind valid", func(t *testing.T) {
		wantPath := filepath.Join(tmp, "newfile.txt")
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: wantPath, Kind: "output-file"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
		if resp.Path != wantPath {
			t.Fatalf("expected path %q, got %q", wantPath, resp.Path)
		}
	})

	t.Run("kind any existing file", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularFile, Kind: "any"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
	})

	t.Run("kind dir existing dir", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularDir, Kind: "dir"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
	})

	t.Run("kind file existing file", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularFile, Kind: "file"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
	})

	t.Run("kind executable executable file", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: executableFile, Kind: "executable"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
	})

	t.Run("kind executable non-executable", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: nonExecutableFile, Kind: "executable"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is not executable" {
			t.Fatalf("expected 'path is not executable', got %q", resp.Error)
		}
	})

	t.Run("unknown kind", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularFile, Kind: "weird"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if !strings.HasPrefix(resp.Error, "unknown path kind") {
			t.Fatalf("expected 'unknown path kind' prefix, got %q", resp.Error)
		}
	})

	t.Run("kind empty defaults to any", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularFile, Kind: ""})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
	})

	t.Run("kind dir on file is invalid", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularFile, Kind: "dir"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is not a directory" {
			t.Fatalf("expected 'path is not a directory', got %q", resp.Error)
		}
	})

	t.Run("kind file on dir is invalid", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularDir, Kind: "file"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is a directory" {
			t.Fatalf("expected 'path is a directory', got %q", resp.Error)
		}
	})

	t.Run("kind executable on dir is invalid", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularDir, Kind: "executable"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is a directory" {
			t.Fatalf("expected 'path is a directory', got %q", resp.Error)
		}
	})

	t.Run("command kind absolute path to directory is invalid", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: regularDir, Kind: "command"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is a directory" {
			t.Fatalf("expected 'path is a directory', got %q", resp.Error)
		}
	})

	t.Run("command kind absolute path to non-executable is invalid", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: nonExecutableFile, Kind: "command"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "path is not executable" {
			t.Fatalf("expected 'path is not executable', got %q", resp.Error)
		}
	})

	t.Run("command kind absolute path to executable is valid", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: executableFile, Kind: "command"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
		}
		if resp.Path != executableFile {
			t.Fatalf("expected path %q, got %q", executableFile, resp.Path)
		}
	})

	t.Run("output-file parent not a directory", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: filepath.Join(regularFile, "child.txt"), Kind: "output-file"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error != "parent path is not a directory" {
			t.Fatalf("expected 'parent path is not a directory', got %q", resp.Error)
		}
	})

	t.Run("output-file missing parent", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: filepath.Join(tmp, "missing", "out.txt"), Kind: "output-file"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error == "" {
			t.Fatal("expected error message")
		}
	})

	t.Run("non-existent path with kind any", func(t *testing.T) {
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: filepath.Join(tmp, "does-not-exist"), Kind: "any"})
		if resp.Valid {
			t.Fatal("expected invalid")
		}
		if resp.Error == "" {
			t.Fatal("expected error message")
		}
	})
}
