package main

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

// errFS is a minimal fs.FS whose WalkDir callback receives an error for the
// root, covering the "err != nil" branch in frontendDistHash (lines 22-23).
type errFS struct{}

func (errFS) Open(name string) (fs.File, error) {
	return nil, errors.New("open error")
}

func TestFrontendDistHash_WalkDirError(t *testing.T) {
	_, err := frontendDistHash(errFS{})
	if err == nil {
		t.Fatalf("frontendDistHash with a failing FS should error")
	}
}

// failingReadFS wraps fstest.MapFS to make ReadFile fail for a specific path,
// covering the ReadFile error branch (line 42-43). We must override ReadFile
// because fstest.MapFS implements fs.ReadFileFS directly.
type failingReadFS struct {
	fstest.MapFS
	failPath string
}

func (f *failingReadFS) ReadFile(name string) ([]byte, error) {
	if name == f.failPath {
		return nil, errors.New("read failed")
	}
	return f.MapFS.ReadFile(name)
}

func TestFrontendDistHash_ReadFileFailure(t *testing.T) {
	base := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>")},
	}
	fsys := &failingReadFS{MapFS: base, failPath: "index.html"}
	_, err := frontendDistHash(fsys)
	if err == nil {
		t.Fatalf("frontendDistHash with failing ReadFile should error")
	}
}
