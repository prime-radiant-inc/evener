package durablefile

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/fsfake"
	"primeradiant.com/evener/execsupport/fsatomic"
)

func TestReplaceSyncsFileThenRenamesThenSyncsDirectory(t *testing.T) {
	t.Parallel()
	fs := fsfake.NewSyncFS(afero.NewMemMapFs())
	path := "/state/tasks/session.json"
	if err := fs.MkdirAll("/state/tasks", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(fs, path, []byte("new"), 0o644); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	ops := fs.Ops()
	syncFile, rename, syncDir := -1, -1, -1
	for i, op := range ops {
		switch {
		case strings.HasPrefix(op, "sync:/state/tasks/session.json.tmp-"):
			syncFile = i
		case strings.HasPrefix(op, "rename:"):
			rename = i
		case op == "sync:/state/tasks":
			syncDir = i
		}
	}
	if syncFile < 0 || rename < 0 || syncDir < 0 {
		t.Fatalf("Replace operations = %v, want file sync, rename, directory sync", ops)
	}
	if !(syncFile < rename && rename < syncDir) {
		t.Fatalf("Replace order = %v, want file sync before rename before directory sync", ops)
	}
}

func TestReplaceDirectorySyncFailureReportsPublished(t *testing.T) {
	t.Parallel()
	fs := fsfake.NewSyncFS(afero.NewMemMapFs())
	fs.FailOpen = func(name string) bool { return name == "/state/tasks" }
	path := "/state/tasks/session.json"
	if err := fs.Fs.MkdirAll("/state/tasks", 0o755); err != nil {
		t.Fatal(err)
	}
	err := Replace(fs, path, []byte("new"), 0o644)
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("Replace error = %v, want fsatomic.ErrPublished", err)
	}
	got, readErr := afero.ReadFile(fs.Fs, path)
	if readErr != nil {
		t.Fatalf("read published file: %v", readErr)
	}
	if string(got) != "new" {
		t.Fatalf("published contents = %q, want new", got)
	}
}

func TestReplaceFileSyncFailureLeavesOldContents(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	path := "/state/tasks/session.json"
	if err := base.MkdirAll("/state/tasks", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(base, path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &failingSyncFileFS{Fs: base}
	err := Replace(fs, path, []byte("new"), 0o644)
	if err == nil {
		t.Fatal("Replace succeeded with a failing file sync")
	}
	if errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("file sync failure reported as published: %v", err)
	}
	got, readErr := afero.ReadFile(base, path)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(got) != "old" {
		t.Fatalf("target contents = %q, want old", got)
	}
}

type failingSyncFileFS struct {
	afero.Fs
}

func (f *failingSyncFileFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &failingSyncFile{File: file}, nil
}

type failingSyncFile struct {
	afero.File
}

func (f *failingSyncFile) Sync() error {
	return errors.New("injected file sync failure")
}
