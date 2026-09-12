//go:build evenerfuzz

package plugins

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type coverageDirEntry struct {
	name string
	dir  bool
}

func (e coverageDirEntry) Name() string               { return e.name }
func (e coverageDirEntry) IsDir() bool                { return e.dir }
func (e coverageDirEntry) Type() fs.FileMode          { return 0 }
func (e coverageDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }

type coverageTempFile struct {
	name     string
	closeErr error
}

func (f coverageTempFile) Name() string { return f.name }
func (f coverageTempFile) Close() error { return f.closeErr }

func FuzzDoctorGCCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Run("doctor", coverageDoctor)
		t.Run("gc", coverageGC)
	})
}

func coverageDoctor(t *testing.T) {
	origReadDir, origStat := doctorReadDir, doctorStat
	origCreateTemp, origRemove := doctorCreateTemp, doctorRemove
	origGitAvailable, origAcquireLock := doctorGitAvailable, doctorAcquireLock
	t.Cleanup(func() {
		doctorReadDir, doctorStat = origReadDir, origStat
		doctorCreateTemp, doctorRemove = origCreateTemp, origRemove
		doctorGitAvailable, doctorAcquireLock = origGitAvailable, origAcquireLock
	})

	root := t.TempDir()
	m := NewManager(root)
	reg := Registry{Plugins: map[string][]InstallEntry{"empty@market": {}}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Doctor(); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	// The walk reaches the store lock only once there is a cache directory to
	// walk, and everything past the lock is stubbed from here on.
	if err := os.MkdirAll(m.cacheDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	doctorAcquireLock = func(context.Context, string, time.Duration) (func(), error) { return nil, boom }
	if got := m.doctorOrphanCacheDirs(); len(got) != 1 || got[0].Level != LevelWarn {
		t.Fatalf("held lock findings = %#v", got)
	}
	doctorAcquireLock = func(context.Context, string, time.Duration) (func(), error) { return func() {}, nil }
	if err := os.WriteFile(m.registryPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.doctorOrphanCacheDirs(); len(got) != 1 || got[0].Level != LevelFail {
		t.Fatalf("corrupt registry findings = %#v", got)
	}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	doctorReadDir = func(path string) ([]os.DirEntry, error) {
		switch filepath.Base(path) {
		case "cache":
			return []os.DirEntry{coverageDirEntry{name: "flat"}, coverageDirEntry{name: "market", dir: true}}, nil
		case "market":
			return nil, boom
		default:
			return nil, boom
		}
	}
	if got := m.doctorOrphanCacheDirs(); len(got) != 0 {
		t.Fatalf("market read error findings = %#v", got)
	}

	doctorReadDir = func(path string) ([]os.DirEntry, error) {
		switch filepath.Base(path) {
		case "cache":
			return []os.DirEntry{coverageDirEntry{name: "market", dir: true}}, nil
		case "market":
			return []os.DirEntry{coverageDirEntry{name: "flat"}, coverageDirEntry{name: "plugin", dir: true}}, nil
		case "plugin":
			return nil, boom
		default:
			return nil, boom
		}
	}
	if got := m.doctorOrphanCacheDirs(); len(got) != 0 {
		t.Fatalf("plugin read error findings = %#v", got)
	}

	doctorReadDir = func(path string) ([]os.DirEntry, error) {
		switch filepath.Base(path) {
		case "cache":
			return []os.DirEntry{coverageDirEntry{name: "market", dir: true}}, nil
		case "market":
			return []os.DirEntry{coverageDirEntry{name: "plugin", dir: true}}, nil
		case "plugin":
			return []os.DirEntry{coverageDirEntry{name: "flat"}, coverageDirEntry{name: "sha", dir: true}}, nil
		default:
			return nil, boom
		}
	}
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{
		"plugin@market": {{InstallPath: filepath.Join(root, "cache", "market", "plugin", "sha"), Source: Source{Kind: SourceGitHub, Repo: "acme/plugin"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := m.doctorOrphanCacheDirs(); len(got) != 0 {
		t.Fatalf("referenced findings = %#v", got)
	}

	doctorGitAvailable = func() bool { return false }
	if got := m.doctorEnvironment(); got[0].Level != LevelWarn {
		t.Fatalf("git finding = %#v", got)
	}

	doctorStat = func(string) (os.FileInfo, error) { return nil, boom }
	if exists, err := m.checkStoreWritable(); exists || !errors.Is(err, boom) {
		t.Fatalf("stat = %v, %v", exists, err)
	}
	doctorStat = os.Stat
	doctorCreateTemp = func(string, string) (doctorTempFile, error) { return nil, boom }
	if exists, err := m.checkStoreWritable(); !exists || !errors.Is(err, boom) {
		t.Fatalf("create = %v, %v", exists, err)
	}
	tmp := filepath.Join(root, "probe")
	doctorCreateTemp = func(string, string) (doctorTempFile, error) {
		return coverageTempFile{name: tmp, closeErr: boom}, nil
	}
	if exists, err := m.checkStoreWritable(); !exists || !errors.Is(err, boom) {
		t.Fatalf("close = %v, %v", exists, err)
	}
	doctorCreateTemp = func(string, string) (doctorTempFile, error) {
		return coverageTempFile{name: tmp}, nil
	}
	doctorRemove = func(string) error { return boom }
	if exists, err := m.checkStoreWritable(); !exists || !errors.Is(err, boom) {
		t.Fatalf("remove = %v, %v", exists, err)
	}
}

func coverageGC(t *testing.T) {
	origLock, origReadDir, origRemoveAll := gcAcquireLock, gcReadDir, gcRemoveAll
	t.Cleanup(func() { gcAcquireLock, gcReadDir, gcRemoveAll = origLock, origReadDir, origRemoveAll })
	boom := errors.New("boom")
	m := NewManager(t.TempDir())

	gcAcquireLock = func(context.Context, string, time.Duration) (func(), error) { return nil, boom }
	if _, err := m.Gc(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("lock: %v", err)
	}
	gcAcquireLock = func(context.Context, string, time.Duration) (func(), error) { return func() {}, nil }
	if err := os.WriteFile(m.registryPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Gc(context.Background()); err == nil {
		t.Fatal("corrupt registry succeeded")
	}
	if err := os.Remove(m.registryPath()); err != nil {
		t.Fatal(err)
	}

	gcReadDir = func(path string) ([]os.DirEntry, error) {
		switch filepath.Base(path) {
		case "cache":
			return []os.DirEntry{coverageDirEntry{name: "flat"}, coverageDirEntry{name: "market", dir: true}}, nil
		case "market":
			return nil, boom
		default:
			return nil, boom
		}
	}
	if got, err := m.Gc(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("market: %#v, %v", got, err)
	}
	gcReadDir = func(path string) ([]os.DirEntry, error) {
		switch filepath.Base(path) {
		case "cache":
			return []os.DirEntry{coverageDirEntry{name: "market", dir: true}}, nil
		case "market":
			return []os.DirEntry{coverageDirEntry{name: "flat"}, coverageDirEntry{name: "plugin", dir: true}}, nil
		case "plugin":
			return nil, boom
		default:
			return nil, boom
		}
	}
	if got, err := m.Gc(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("plugin: %#v, %v", got, err)
	}
	gcReadDir = func(path string) ([]os.DirEntry, error) {
		switch filepath.Base(path) {
		case "cache":
			return []os.DirEntry{coverageDirEntry{name: "market", dir: true}}, nil
		case "market":
			return []os.DirEntry{coverageDirEntry{name: "plugin", dir: true}}, nil
		case "plugin":
			return []os.DirEntry{coverageDirEntry{name: "flat"}, coverageDirEntry{name: "sha", dir: true}}, nil
		default:
			return nil, boom
		}
	}
	gcRemoveAll = func(string) error { return boom }
	if _, err := m.Gc(context.Background()); err == nil {
		t.Fatal("remove succeeded")
	}
}
