package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

func TestGrepStopsWhenContextIsCanceled(t *testing.T) {
	root := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "match.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := NewLocalExecutionEnvironment(root)
	env.Sandbox = resolvePolicy(t, sandbox.ModeRestricted, filepath.Dir(root), root)
	t.Cleanup(env.Cleanup)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	walk := secureBrowseWalkDir
	secureBrowseWalkDir = func(fsys fs.FS, name string, fn fs.WalkDirFunc) error {
		return walk(fsys, name, func(path string, entry fs.DirEntry, err error) error {
			if path == "." {
				cancel()
			}
			return fn(path, entry, err)
		})
	}
	t.Cleanup(func() { secureBrowseWalkDir = walk })

	_, err := env.Grep(ctx, "needle", root, "", false, 100, "files_with_matches")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Grep error = %v, want context.Canceled", err)
	}
}

func TestUnsandboxedGrepStopsWhenContextIsCanceled(t *testing.T) {
	root := realTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "match.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := NewLocalExecutionEnvironment(root)
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	walk := grepWalk
	grepWalk = func(fsys fs.FS, name string, fn fs.WalkDirFunc) error {
		return walk(fsys, name, func(path string, entry fs.DirEntry, err error) error {
			if path == "." {
				cancel()
			}
			return fn(path, entry, err)
		})
	}
	t.Cleanup(func() { grepWalk = walk })

	_, err := env.Grep(ctx, "needle", root, "", false, 100, "files_with_matches")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Grep error = %v, want context.Canceled", err)
	}
}
