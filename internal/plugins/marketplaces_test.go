package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeMarketplaceRepo builds a git repo containing a .claude-plugin/marketplace.json
// naming one plugin, and returns its path.
func makeMarketplaceRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mkt-"+name)
	mj := `{"name":"` + name + `","owner":{"name":"o"},"plugins":[` +
		`{"name":"widget","description":"a widget","source":"./plugins/widget"}]}`
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, dir, "README.md", "mkt") // also commits marketplace.json via `git add .`
	return dir
}

func TestAddListRemoveMarketplace(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	src := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())

	ref, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: src})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if ref.InstallLocation == "" {
		t.Fatal("empty InstallLocation")
	}

	list, err := m.ListMarketplaces()
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if _, ok := list["acme"]; !ok {
		t.Fatalf("marketplace 'acme' not listed: %v", list)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	list, _ = m.ListMarketplaces()
	if _, ok := list["acme"]; ok {
		t.Fatal("marketplace still present after remove")
	}
	if _, err := os.Stat(m.marketplaceDir("acme")); !os.IsNotExist(err) {
		t.Fatal("clone dir not deleted after remove")
	}
}

func TestAddMarketplace_GitSubdirBrowse(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "monorepo")
	if err := os.MkdirAll(filepath.Join(repo, "mkt", ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mkt", ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, repo, "README.md", "root") // commits everything incl. mkt/

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceGitSubdir, URL: repo, Path: "mkt"}); err != nil {
		t.Fatalf("AddMarketplace git-subdir: %v", err)
	}
	cat, err := m.Browse(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Browse git-subdir marketplace: %v", err)
	}
	if cat.Name != "acme" || len(cat.Plugins) != 1 {
		t.Fatalf("catalog = %+v", cat)
	}
}

func TestAddMarketplace_RejectsTraversalName(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "evilmkt")
	if err := os.MkdirAll(filepath.Join(repo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// marketplace.json whose name escapes the store
	if err := os.WriteFile(filepath.Join(repo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"../../evil","owner":{"name":"o"},"plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, repo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: repo}); err == nil {
		t.Fatal("AddMarketplace accepted a traversing marketplace name")
	}
}

func TestRemoveMarketplace_DirectorySourceKeepsContents(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := makeMarketplaceRepo(t, "local")
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace directory: %v", err)
	}
	if err := m.RemoveMarketplace(context.Background(), "local"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatalf("directory source contents deleted on remove: %v", err)
	}
}

func TestRefreshMarketplace_ClonesUnfetchedSeed(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())
	if err := m.saveMarketplaces(Marketplaces{"acme": {Source: Source{Kind: SourceURL, URL: mktRepo}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("refresh unfetched seed: %v", err)
	}
	mk, _ := m.ListMarketplaces()
	if mk["acme"].InstallLocation == "" {
		t.Fatal("refresh did not clone the unfetched seed")
	}
}

// TestRefreshMarketplace_RecoversWedgedCloneByStagedReclone pins the
// self-heal path: when git pull fails in the persistent clone (here wedged
// exactly the way a SIGKILLed git wedges it — a stale .git/index.lock),
// refresh falls back to a staged reclone and recovers instead of failing
// forever until the marketplace is removed and re-added.
func TestRefreshMarketplace_RecoversWedgedCloneByStagedReclone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	src := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())
	ref, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: src})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	// Upstream gains a commit, so a refresh must move the index (and would
	// visibly pull new content).
	advanceGitRepo(t, src, "README.md", "new upstream content")
	// Wedge the clone: a stale lock makes every pull fail.
	if err := os.WriteFile(filepath.Join(ref.InstallLocation, ".git", "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := m.RefreshMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("refresh did not self-heal a wedged clone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(ref.InstallLocation, "README.md")); err != nil || string(b) != "new upstream content" {
		t.Fatalf("recovered clone lacks fresh upstream content: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(ref.InstallLocation, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatalf("stale index.lock survived the reclone: %v", err)
	}
	for _, leftover := range []string{".staging", ".old"} {
		if _, err := os.Stat(m.marketplaceDir(leftover)); !os.IsNotExist(err) {
			t.Fatalf("reclone left %s behind: %v", leftover, err)
		}
	}
}

// TestRefreshMarketplace_FailedRecloneLeavesCloneUntouched pins the safety
// constraint of the staged reclone: if the fresh clone cannot be downloaded,
// the existing (possibly wedged) clone is left exactly as it was — never
// wiped — and the error reports both failures loudly.
func TestRefreshMarketplace_FailedRecloneLeavesCloneUntouched(t *testing.T) {
	m := NewManager(t.TempDir())
	installLoc := m.marketplaceDir("acme")
	if err := os.MkdirAll(installLoc, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(installLoc, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := time.Unix(1000, 0).UTC()
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"},
		InstallLocation: installLoc,
		LastUpdated:     before,
	}}); err != nil {
		t.Fatal(err)
	}
	origPull, origClone := marketplaceGitPull, marketplaceGitClone
	t.Cleanup(func() { marketplaceGitPull, marketplaceGitClone = origPull, origClone })
	marketplaceGitPull = func(context.Context, string) error { return errors.New("pull wedged") }
	marketplaceGitClone = func(context.Context, string, string, string, string) error { return errors.New("network down") }

	err := m.RefreshMarketplace(context.Background(), "acme")
	if err == nil {
		t.Fatal("refresh succeeded; want loud error when pull and reclone both fail")
	}
	if !strings.Contains(err.Error(), "pull wedged") || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("err = %v, want both the pull and reclone failures reported", err)
	}
	b, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(b) != "still here" {
		t.Fatalf("old clone touched by failed reclone: %q, %v", b, readErr)
	}
	if _, err := os.Stat(m.marketplaceDir(".staging")); !os.IsNotExist(err) {
		t.Fatalf("failed reclone left .staging behind: %v", err)
	}
	mk, _ := m.ListMarketplaces()
	if !mk["acme"].LastUpdated.Equal(before) {
		t.Fatalf("LastUpdated advanced on failed refresh: %v", mk["acme"].LastUpdated)
	}
}
