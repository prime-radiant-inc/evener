package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestGrepRipgrepStopsWhenContextIsCanceled(t *testing.T) {
	root := realTempDir(t)
	rg := filepath.Join(root, "rg")
	if err := os.WriteFile(rg, []byte("#!/bin/sh\n: > .rg-started\ntrap 'exit 130' TERM INT\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	env.lookPath = func(string) (string, error) { return rg, nil }
	t.Cleanup(env.Cleanup)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := env.Grep(ctx, "needle", root, "", false, 100, "files_with_matches")
		done <- err
	}()

	deadline := time.NewTimer(10 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, ".rg-started")); err == nil {
			cancel()
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("ripgrep command did not start")
		case err := <-done:
			t.Fatalf("ripgrep command exited before cancellation: %v", err)
		case <-ticker.C:
		}
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ripgrep Grep error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ripgrep Grep did not stop after context cancellation")
	}
}
