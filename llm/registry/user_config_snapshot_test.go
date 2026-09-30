package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestUserConfigSnapshotIsTheSuccessfullyLoadedFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "providers.toml")
	original := []byte("[providers.work]\nbase = \"ollama\"\ndefault_model = \"fixture\"\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	r := loadSnapshotTestRegistry(t, path)
	if err := os.WriteFile(path, []byte("[providers.broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, got := r.UserConfigSnapshot()
	if source != path {
		t.Fatal("snapshot lost its successful source path")
	}
	if !bytes.Equal(got, original) {
		t.Fatal("snapshot did not retain the successfully parsed bytes")
	}
	got[0] = '!'
	_, second := r.UserConfigSnapshot()
	if !bytes.Equal(second, original) {
		t.Fatal("snapshot accessor exposed mutable registry bytes")
	}
}

func TestUserConfigSnapshotDistinguishesEmptyMissingAndDisabled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "providers.toml")
	if _, got := loadSnapshotTestRegistry(t, path).UserConfigSnapshot(); got != nil {
		t.Fatal("missing file has a snapshot")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, got := loadSnapshotTestRegistry(t, path).UserConfigSnapshot(); got == nil || len(got) != 0 {
		t.Fatal("successful empty file lost its presence")
	}
	if _, got := loadSnapshotTestRegistry(t, path, WithNoUserLayer()).UserConfigSnapshot(); got != nil {
		t.Fatal("disabled user layer has a snapshot")
	}
}

func loadSnapshotTestRegistry(t *testing.T, path string, extra ...Option) *Registry {
	t.Helper()
	opts := []Option{WithConfigPath(path), WithEnv(mapEnv(nil)), WithStateRoot(t.TempDir()), WithOffline(true), WithoutCache()}
	r, err := Load(append(opts, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
