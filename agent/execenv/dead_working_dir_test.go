package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// This file is the behavior surface for issue #4671 surface 3: a relative-path
// file mutation whose environment working directory has been deleted must
// refuse and must not recreate the directory (the "silently writes in the wrong
// place" failure).

// T3: every mutation entry refuses a relative path under a dead working
// directory and leaves the deleted directory deleted.
func TestMutations_RefuseRelativePathUnderDeadWorkingDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env := NewLocalExecutionEnvironment(root)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove working directory: %v", err)
	}

	cases := []struct {
		name string
		call func() error
	}{
		{"WriteFileRaw", func() error { return env.WriteFileRaw("nested/file.txt", []byte("x"), 0o644) }},
		{"WriteFile", func() error { _, err := env.WriteFile("nested/file.txt", "x"); return err }},
		{"EditFile", func() error { _, err := env.EditFile("nested/file.txt", "a", "b", false); return err }},
		{"RemovePath", func() error { return env.RemovePath("nested/file.txt") }},
		{"RenamePath", func() error { return env.RenamePath("a.txt", "nested/b.txt") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, ErrWorkingDirectoryUnavailable) {
				t.Fatalf("%s error = %v, want ErrWorkingDirectoryUnavailable", tc.name, err)
			}
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("%s recreated the deleted working directory: stat err = %v", tc.name, statErr)
			}
		})
	}
}

// An absolute-path mutation is unaffected by a dead working directory.
func TestMutations_AbsolutePathUnaffectedByDeadWorkingDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env := NewLocalExecutionEnvironment(root)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove working directory: %v", err)
	}
	target := filepath.Join(t.TempDir(), "abs", "file.txt")
	if err := env.WriteFileRaw(target, []byte("payload"), 0o644); err != nil {
		t.Fatalf("absolute WriteFileRaw under a dead working dir = %v, want success", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written absolute file: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("absolute file contents = %q, want %q", got, "payload")
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("absolute write recreated the working directory: stat err = %v", statErr)
	}
}

// With a live working directory, a nested relative write still creates parents
// exactly as before.
func TestMutations_LiveWorkingDirCreatesParents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env := NewLocalExecutionEnvironment(root)
	if err := env.WriteFileRaw("nested/file.txt", []byte("payload"), 0o644); err != nil {
		t.Fatalf("relative write with a live working dir = %v, want success", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "nested", "file.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("file contents = %q, want %q", got, "payload")
	}
}
