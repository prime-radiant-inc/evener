package fspaths_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
)

func TestCompleteDirs(t *testing.T) {
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

	t.Run("empty prefix returns home dir entries", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) == 0 {
			t.Fatal("expected home dir entries")
		}
		// Should contain Alpha, Beta, Gamma, delta (non-hidden).
		// .hidden should be excluded when filter is empty.
		for _, p := range resp.Data {
			base := filepath.Base(p)
			if strings.HasPrefix(base, ".") {
				t.Fatalf("hidden dir %q should be excluded", base)
			}
		}
	})

	t.Run("prefix with ~/ expands to home", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: "~/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) == 0 {
			t.Fatal("expected entries after ~ expansion")
		}
	})

	t.Run("prefix ending with slash lists that dir", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: home + "/"})
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
	})

	t.Run("prefix without trailing slash lists parent and filters", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: filepath.Join(home, "Al")})
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
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: "/nonexistent-dir-12345/xyz"})
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if len(resp.Data) != 0 {
			t.Fatalf("expected empty response for unreadable dir, got %v", resp.Data)
		}
	})

	t.Run("limit greater than 30 is capped", func(t *testing.T) {
		// Create more than 30 uniquely-named dirs so the cap is the only
		// thing that can hold the result count down to 30.
		bigHome := t.TempDir()
		t.Setenv("HOME", bigHome)
		for i := 0; i < 40; i++ {
			if err := os.MkdirAll(filepath.Join(bigHome, fmt.Sprintf("dir%02d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: bigHome + "/", Limit: 100})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 30 {
			t.Fatalf("expected exactly 30 entries (capped), got %d", len(resp.Data))
		}
	})

	t.Run("hidden dirs filtered when no filter", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: home + "/"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, p := range resp.Data {
			if strings.HasPrefix(filepath.Base(p), ".") {
				t.Fatalf("hidden dir %q should be filtered", p)
			}
		}
	})

	t.Run("hidden dirs included when filter matches", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: filepath.Join(home, ".hid")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 1 || filepath.Base(resp.Data[0]) != ".hidden" {
			t.Fatalf("expected [.hidden], got %v", resp.Data)
		}
	})

	t.Run("case-insensitive filter matching", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: filepath.Join(home, "aL")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 1 || filepath.Base(resp.Data[0]) != "Alpha" {
			t.Fatalf("expected [Alpha], got %v", resp.Data)
		}
	})

	t.Run("results are sorted", func(t *testing.T) {
		resp, err := fspaths.CompleteDirs(appwire.DirsCompleteParams{Prefix: home + "/"})
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

func TestValidateLaunchPath(t *testing.T) {
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
		if resp.Path == "" {
			t.Fatal("expected resolved path")
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
		resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: filepath.Join(tmp, "newfile.txt"), Kind: "output-file"})
		if !resp.Valid {
			t.Fatalf("expected valid, got error: %s", resp.Error)
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
