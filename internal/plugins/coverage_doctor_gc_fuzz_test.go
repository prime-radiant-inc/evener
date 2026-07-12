//go:build serffuzz

package plugins

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentplugin "primeradiant.com/serf/agent/plugin"
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
		t.Run("enabled", coverageEnabled)
	})
}

func coverageDoctor(t *testing.T) {
	origReadDir, origStat := doctorReadDir, doctorStat
	origCreateTemp, origRemove := doctorCreateTemp, doctorRemove
	origGitAvailable := doctorGitAvailable
	t.Cleanup(func() {
		doctorReadDir, doctorStat = origReadDir, origStat
		doctorCreateTemp, doctorRemove = origCreateTemp, origRemove
		doctorGitAvailable = origGitAvailable
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
	if got := m.doctorOrphanCacheDirs(nil); len(got) != 0 {
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
	if got := m.doctorOrphanCacheDirs(nil); len(got) != 0 {
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
	known := map[string]bool{filepath.Join(root, "cache", "market", "plugin", "sha"): true}
	if got := m.doctorOrphanCacheDirs(known); len(got) != 0 {
		t.Fatalf("known findings = %#v", got)
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

	gcAcquireLock = func(string, time.Duration) (func(), error) { return nil, boom }
	if _, err := m.Gc(); !errors.Is(err, boom) {
		t.Fatalf("lock: %v", err)
	}
	gcAcquireLock = func(string, time.Duration) (func(), error) { return func() {}, nil }
	if err := os.WriteFile(m.registryPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Gc(); err == nil {
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
	if got, err := m.Gc(); err != nil || len(got) != 0 {
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
	if got, err := m.Gc(); err != nil || len(got) != 0 {
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
	if _, err := m.Gc(); err == nil {
		t.Fatal("remove succeeded")
	}
}

func coverageEnabled(t *testing.T) {
	origLoad := enabledLoad
	t.Cleanup(func() { enabledLoad = origLoad })
	root := t.TempDir()
	var stderr bytes.Buffer
	m := &Manager{Root: root, Stderr: &stderr}
	pluginDir := t.TempDir()
	writePlugin(t, pluginDir, "p", nil)
	reg := Registry{Plugins: map[string][]InstallEntry{"p@m": {{Enabled: true, InstallPath: pluginDir, Source: Source{Kind: SourceDirectory, Path: pluginDir}}}}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}
	enabledLoad = func(string) (agentplugin.Instance, error) { return agentplugin.Instance{}, errors.New("boom") }
	m.EnabledPluginDirs(nil)
	if !strings.Contains(stderr.String(), "skipping broken plugin") {
		t.Fatalf("warning = %q", stderr.String())
	}
}
