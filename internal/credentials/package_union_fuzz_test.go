//go:build serffuzz

package credentials

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
)

// FuzzPackageUnion replays every deterministic package scenario under fuzz coverage.

func FuzzPackageUnion(f *testing.F) {

	f.Add(uint8(0))

	f.Fuzz(func(t *testing.T, _ uint8) {

		t.Run("TestStore_LoadMissingFile", TestStore_LoadMissingFile)
		t.Run("TestStore_SetGetClear", TestStore_SetGetClear)
		t.Run("TestStore_PermissionsEnforced", TestStore_PermissionsEnforced)
		t.Run("TestStore_GetFallsBackToEnv", TestStore_GetFallsBackToEnv)
		t.Run("TestStore_OpenAICompatibleUsesAPIKeyEnv", TestStore_OpenAICompatibleUsesAPIKeyEnv)
		t.Run("TestResolveKeyNameThenTypeEnv", TestResolveKeyNameThenTypeEnv)
		t.Run("TestResolveKeyOpenAICompatibleUsesCompatEnv", TestResolveKeyOpenAICompatibleUsesCompatEnv)
		t.Run("TestStore_List", TestStore_List)
		t.Run("failure paths", fuzzStoreFailurePaths)
	})
}

type faultFS struct {
	afero.Fs
	op string
}

func (fs faultFS) Stat(name string) (os.FileInfo, error) {
	if fs.op == "stat" {
		return nil, errors.New("stat fault")
	}
	return fs.Fs.Stat(name)
}
func (fs faultFS) Open(name string) (afero.File, error) {
	if fs.op == "read" {
		return nil, errors.New("read fault")
	}
	return fs.Fs.Open(name)
}
func (fs faultFS) MkdirAll(path string, perm os.FileMode) error {
	if fs.op == "mkdir" {
		return errors.New("mkdir fault")
	}
	return fs.Fs.MkdirAll(path, perm)
}
func (fs faultFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if fs.op == "open" {
		return nil, errors.New("open fault")
	}
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: f, op: fs.op}, nil
}
func (fs faultFS) Rename(oldname, newname string) error {
	if fs.op == "rename" {
		return errors.New("rename fault")
	}
	return fs.Fs.Rename(oldname, newname)
}

type faultFile struct {
	afero.File
	op string
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.op == "write" {
		return 0, errors.New("write fault")
	}
	return f.File.Write(p)
}
func (f *faultFile) Sync() error {
	if f.op == "sync" {
		return errors.New("sync fault")
	}
	return f.File.Sync()
}
func (f *faultFile) Close() error {
	if f.op == "close" {
		_ = f.File.Close()
		return errors.New("close fault")
	}
	return f.File.Close()
}

func fuzzStoreFailurePaths(t *testing.T) {
	base := afero.NewMemMapFs()
	if _, err := loadStoreFS(faultFS{Fs: base, op: "stat"}, "/credentials.toml"); err == nil {
		t.Fatal("stat fault succeeded")
	}
	if err := afero.WriteFile(base, "/credentials.toml", []byte("schema = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStoreFS(faultFS{Fs: base, op: "read"}, "/credentials.toml"); err == nil {
		t.Fatal("read fault succeeded")
	}
	for _, op := range []string{"mkdir", "open", "write", "sync", "close", "rename"} {
		t.Run(op, func(t *testing.T) {
			s := &Store{path: "/dir/credentials.toml", fs: faultFS{Fs: afero.NewMemMapFs(), op: op}, data: fileShape{Schema: 1}}
			if err := s.Set("openai", "key"); err == nil {
				t.Fatalf("%s fault succeeded", op)
			}
		})
	}
	s := &Store{fs: base, data: fileShape{Schema: 1}}
	if err := s.Set("OPENAI", " value "); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", " env ")
	if hasFile, env := s.Layers("openai"); !hasFile || env != "OPENAI_API_KEY" {
		t.Fatalf("Layers = %v, %q", hasFile, env)
	}
	if hasFile, env := s.InstanceLayers("custom", "openai"); hasFile || env != "OPENAI_API_KEY" {
		t.Fatalf("InstanceLayers = %v, %q", hasFile, env)
	}
}
