//go:build serffuzz

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func FuzzPluginsMarketplacesCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) { fuzzMarketplacesCoverage(t) })
}

func fuzzMarketplacesCoverage(t *testing.T) {
	origRead, origMarshal := marketplaceReadFile, marketplaceMarshalIndent
	origWrite, origClone := marketplaceAtomicWriteFile, marketplaceGitClone
	origSparse, origPull := marketplaceGitSparseClone, marketplaceGitPull
	origRemove, origRename := marketplaceRemoveAll, marketplaceRename
	origLock, origStat := marketplaceAcquireLock, marketplaceStat
	t.Cleanup(func() {
		marketplaceReadFile, marketplaceMarshalIndent = origRead, origMarshal
		marketplaceAtomicWriteFile, marketplaceGitClone = origWrite, origClone
		marketplaceGitSparseClone, marketplaceGitPull = origSparse, origPull
		marketplaceRemoveAll, marketplaceRename = origRemove, origRename
		marketplaceAcquireLock, marketplaceStat = origLock, origStat
	})
	reset := func() {
		marketplaceReadFile, marketplaceMarshalIndent = origRead, origMarshal
		marketplaceAtomicWriteFile, marketplaceGitClone = origWrite, origClone
		marketplaceGitSparseClone, marketplaceGitPull = origSparse, origPull
		marketplaceRemoveAll, marketplaceRename = origRemove, origRename
		marketplaceAcquireLock, marketplaceStat = origLock, origStat
	}
	fail := errors.New("injected")
	ctx := context.Background()
	m := NewManager(t.TempDir())
	m.Now = func() time.Time { return time.Unix(123, 0) }
	if got := m.catalogRoot(MarketplaceRef{Source: Source{Kind: SourceGitSubdir, Path: "sub"}, InstallLocation: "root"}); got != filepath.Join("root", "sub") {
		t.Fatalf("subdir catalog root = %q", got)
	}
	if got := m.catalogRoot(MarketplaceRef{InstallLocation: "root"}); got != "root" {
		t.Fatalf("catalog root = %q", got)
	}
	if _, err := m.ListMarketplaces(); err != nil {
		t.Fatal(err)
	}

	marketplaceReadFile = func(string) ([]byte, error) { return nil, fail }
	if _, err := m.loadMarketplaces(); err == nil {
		t.Fatal("read error accepted")
	}
	marketplaceReadFile = func(string) ([]byte, error) { return []byte("null"), nil }
	if got, err := m.loadMarketplaces(); err != nil || got == nil {
		t.Fatalf("null load: %#v %v", got, err)
	}
	marketplaceReadFile = func(string) ([]byte, error) { return []byte("{"), nil }
	if _, err := m.loadMarketplaces(); err == nil {
		t.Fatal("bad JSON accepted")
	}
	reset()
	marketplaceMarshalIndent = func(any, string, string) ([]byte, error) { return nil, fail }
	if err := m.saveMarketplaces(Marketplaces{}); err == nil {
		t.Fatal("marshal error accepted")
	}
	reset()

	cloneErr := func(context.Context, string, string, string, string) error { return fail }
	marketplaceGitClone = cloneErr
	for _, src := range []Source{{Kind: SourceGitHub, Repo: "o/r"}, {Kind: SourceURL, URL: "u"}} {
		if _, err := m.fetchMarketplaceContainer(ctx, src, "dst"); err == nil {
			t.Fatalf("clone error accepted for %s", src.Kind)
		}
	}
	marketplaceGitSparseClone = func(context.Context, string, string, string, string, string) error { return fail }
	if _, err := m.fetchMarketplaceContainer(ctx, Source{Kind: SourceGitSubdir, URL: "u", Path: "p"}, "dst"); err == nil {
		t.Fatal("sparse error accepted")
	}
	if _, err := m.fetchMarketplaceContainer(ctx, Source{Kind: "bad"}, "dst"); err == nil {
		t.Fatal("bad kind accepted")
	}
	reset()
	marketplaceGitClone = func(context.Context, string, string, string, string) error { return nil }
	for _, src := range []Source{{Kind: SourceDirectory, Path: "local"}, {Kind: SourceGitHub, Repo: "o/r"}, {Kind: SourceURL, URL: "u"}} {
		if _, err := m.fetchMarketplaceContainer(ctx, src, "dst"); err != nil {
			t.Fatal(err)
		}
	}
	marketplaceGitSparseClone = func(context.Context, string, string, string, string, string) error { return nil }
	if got, err := m.fetchMarketplaceContainer(ctx, Source{Kind: SourceGitSubdir, URL: "u", Path: "p"}, "dst"); err != nil || got != filepath.Join("dst", "p") {
		t.Fatalf("sparse = %q, %v", got, err)
	}
	reset()

	marketplaceReadFile = func(string) ([]byte, error) { return nil, fail }
	if _, err := m.ensureFetched(ctx, "x"); err == nil {
		t.Fatal("ensure load error accepted")
	}
	marketplaceReadFile = func(string) ([]byte, error) { return []byte(`{}`), nil }
	if _, err := m.ensureFetched(ctx, "x"); err == nil {
		t.Fatal("missing ensure accepted")
	}
	installed, _ := json.Marshal(Marketplaces{"x": {Source: Source{Kind: SourceDirectory, Path: "here"}, InstallLocation: "here"}})
	marketplaceReadFile = func(string) ([]byte, error) { return installed, nil }
	if got, err := m.ensureFetched(ctx, "x"); err != nil || got.InstallLocation != "here" {
		t.Fatalf("installed ensure: %#v %v", got, err)
	}
	dirBody, _ := json.Marshal(Marketplaces{"x": {Source: Source{Kind: SourceDirectory, Path: "local"}}})
	marketplaceReadFile = func(string) ([]byte, error) { return dirBody, nil }
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return fail }
	if _, err := m.ensureFetched(ctx, "x"); err == nil {
		t.Fatal("ensure save error accepted")
	}
	gitBody, _ := json.Marshal(Marketplaces{"x": {Source: Source{Kind: SourceURL, URL: "u"}}})
	marketplaceReadFile = func(string) ([]byte, error) { return gitBody, nil }
	marketplaceGitClone = cloneErr
	if _, err := m.ensureFetched(ctx, "x"); err == nil {
		t.Fatal("ensure clone error accepted")
	}
	marketplaceGitClone = func(context.Context, string, string, string, string) error { return nil }
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return nil }
	if got, err := m.ensureFetched(ctx, "x"); err != nil || got.InstallLocation == "" {
		t.Fatalf("ensure clone success: %#v %v", got, err)
	}
	reset()

	marketplaceAcquireLock = func(string, time.Duration) (func(), error) { return nil, fail }
	if _, err := m.AddMarketplace(ctx, "x", Source{Kind: SourceDirectory, Path: "p"}); err == nil {
		t.Fatal("add lock error accepted")
	}
	if err := m.RemoveMarketplace("x"); err == nil {
		t.Fatal("remove lock error accepted")
	}
	if err := m.RefreshMarketplace(ctx, "x"); err == nil {
		t.Fatal("refresh lock error accepted")
	}
	reset()

	root := t.TempDir()
	writeCatalog := func(dir, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"name":"` + name + `","plugins":[]}`
		if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog(root, "catalog")
	for _, tc := range []struct {
		name string
		src  Source
	}{
		{"given", Source{Kind: SourceDirectory, Path: root}},
		{"", Source{Kind: SourceDirectory, Path: root}},
	} {
		mm := NewManager(t.TempDir())
		mm.Now = m.Now
		if _, err := mm.AddMarketplace(ctx, tc.name, tc.src); err != nil {
			t.Fatalf("add directory: %v", err)
		}
	}
	badRoot := t.TempDir()
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "x", Source{Kind: SourceDirectory, Path: badRoot}); err == nil {
		t.Fatal("bad catalog accepted")
	}
	emptyRoot := t.TempDir()
	writeCatalog(emptyRoot, "")
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: emptyRoot}); err == nil {
		t.Fatal("nameless catalog accepted")
	}
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "../bad", Source{Kind: SourceDirectory, Path: root}); err == nil {
		t.Fatal("bad name accepted")
	}

	marketplaceGitClone = cloneErr
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "x", Source{Kind: SourceURL, URL: "u"}); err == nil {
		t.Fatal("add clone error accepted")
	}
	reset()
	staged := t.TempDir()
	writeCatalog(staged, "x")
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error {
		return copyDirForMarketplaceCoverage(staged, dest)
	}
	marketplaceRename = func(string, string) error { return fail }
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "x", Source{Kind: SourceURL, URL: "u"}); err == nil {
		t.Fatal("rename error accepted")
	}
	reset()
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error {
		return copyDirForMarketplaceCoverage(staged, dest)
	}
	marketplaceReadFile = func(string) ([]byte, error) { return nil, fail }
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "x", Source{Kind: SourceURL, URL: "u"}); err == nil {
		t.Fatal("non-directory load error accepted")
	}
	reset()
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error {
		return copyDirForMarketplaceCoverage(staged, dest)
	}
	marketplaceReadFile = func(string) ([]byte, error) { return []byte(`{}`), nil }
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return fail }
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "x", Source{Kind: SourceURL, URL: "u"}); err == nil {
		t.Fatal("non-directory save error accepted")
	}
	reset()

	marketplaceReadFile = func(string) ([]byte, error) { return nil, fail }
	if _, err := NewManager(t.TempDir()).AddMarketplace(ctx, "x", Source{Kind: SourceDirectory, Path: root}); err == nil {
		t.Fatal("add load error accepted")
	}
	if err := NewManager(t.TempDir()).RemoveMarketplace("x"); err == nil {
		t.Fatal("remove load error accepted")
	}
	if err := NewManager(t.TempDir()).RefreshMarketplace(ctx, "x"); err == nil {
		t.Fatal("refresh load error accepted")
	}
	reset()

	missing := NewManager(t.TempDir())
	if err := missing.RemoveMarketplace("x"); err == nil {
		t.Fatal("remove missing accepted")
	}
	if err := missing.RefreshMarketplace(ctx, "x"); err == nil {
		t.Fatal("refresh missing accepted")
	}

	dm := NewManager(t.TempDir())
	dm.Now = m.Now
	if _, err := dm.AddMarketplace(ctx, "x", Source{Kind: SourceDirectory, Path: root}); err != nil {
		t.Fatal(err)
	}
	if err := dm.RefreshMarketplace(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if err := dm.RemoveMarketplace("x"); err != nil {
		t.Fatal(err)
	}
	removeBody, _ := json.Marshal(Marketplaces{"x": {Source: Source{Kind: SourceURL, URL: "u"}, InstallLocation: "old"}})
	marketplaceReadFile = func(string) ([]byte, error) { return removeBody, nil }
	marketplaceRemoveAll = func(string) error { return fail }
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return nil }
	if err := NewManager(t.TempDir()).RemoveMarketplace("x"); err != nil {
		t.Fatal(err)
	}
	reset()

	refreshBody, _ := json.Marshal(Marketplaces{"x": {Source: Source{Kind: SourceURL, URL: "u"}, InstallLocation: "old"}})
	marketplaceReadFile = func(string) ([]byte, error) { return refreshBody, nil }
	marketplaceGitPull = func(context.Context, string) error { return fail }
	if err := NewManager(t.TempDir()).RefreshMarketplace(ctx, "x"); err == nil {
		t.Fatal("pull error accepted")
	}
	reset()
	unfetchedBody, _ := json.Marshal(Marketplaces{"x": {Source: Source{Kind: SourceURL, URL: "u"}}})
	marketplaceReadFile = func(string) ([]byte, error) { return unfetchedBody, nil }
	marketplaceGitClone = cloneErr
	if err := NewManager(t.TempDir()).RefreshMarketplace(ctx, "x"); err == nil {
		t.Fatal("refresh clone error accepted")
	}
	marketplaceGitClone = func(context.Context, string, string, string, string) error { return nil }
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return nil }
	if err := NewManager(t.TempDir()).RefreshMarketplace(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	reset()
	marketplaceReadFile = func(string) ([]byte, error) { return dirBody, nil }
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return fail }
	if err := NewManager(t.TempDir()).RefreshMarketplace(ctx, "x"); err == nil {
		t.Fatal("refresh save error accepted")
	}
	reset()

	marketplaceStat = func(string) (os.FileInfo, error) { return nil, fail }
	if _, err := m.SeedDefaultMarketplaces(); err == nil {
		t.Fatal("seed stat error accepted")
	}
	reset()
	marketplaceStat = func(string) (os.FileInfo, error) { return nil, nil }
	if seeded, err := m.SeedDefaultMarketplaces(); err != nil || seeded {
		t.Fatalf("existing seed = %v, %v", seeded, err)
	}
	reset()
	marketplaceAcquireLock = func(string, time.Duration) (func(), error) { return nil, fail }
	if _, err := NewManager(t.TempDir()).SeedDefaultMarketplaces(); err == nil {
		t.Fatal("seed lock error accepted")
	}
	reset()
	calls := 0
	marketplaceStat = func(string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return nil, fail
	}
	if _, err := NewManager(t.TempDir()).SeedDefaultMarketplaces(); err == nil {
		t.Fatal("seed recheck error accepted")
	}
	reset()
	calls = 0
	marketplaceStat = func(string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return nil, nil
	}
	if seeded, err := NewManager(t.TempDir()).SeedDefaultMarketplaces(); err != nil || seeded {
		t.Fatalf("seed recheck = %v, %v", seeded, err)
	}
	reset()
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return fail }
	if _, err := NewManager(t.TempDir()).SeedDefaultMarketplaces(); err == nil {
		t.Fatal("seed save error accepted")
	}
	reset()
	if seeded, err := NewManager(t.TempDir()).SeedDefaultMarketplaces(); err != nil || !seeded {
		t.Fatalf("seed success = %v, %v", seeded, err)
	}
}

func copyDirForMarketplaceCoverage(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode())
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, body, info.Mode())
	})
}
