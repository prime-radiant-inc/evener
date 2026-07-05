package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// advanceGitRepo commits a change to file in dir, advancing HEAD.
func advanceGitRepo(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "commit", "-aqm", "advance")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

// makeGitBackedMarketplace builds a marketplace repo whose one named plugin is
// a separate git repo (so it has a sha and can be upgraded independently of
// the marketplace). Returns the marketplace repo path and the plugin's own
// repo path.
func makeGitBackedMarketplace(t *testing.T, plugin string) (mktRepo, pluginRepo string) {
	t.Helper()
	pluginRepo = filepath.Join(t.TempDir(), plugin+"repo")
	writePlugin(t, pluginRepo, plugin, nil)
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo = filepath.Join(t.TempDir(), "mkt")
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"` + plugin + `","source":{"source":"url","url":"` + pluginRepo + `"}}]}`
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, mktRepo, "README.md", "x")
	return mktRepo, pluginRepo
}

func TestUpdateAutoUpgrade_OnlyTouchesAutoUpgradeEnabled(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, pluginRepo := makeGitBackedMarketplace(t, "widget")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	// autoUpgrade defaults to false at install; confirm the daemon leaves it alone.
	advanceGitRepo(t, pluginRepo, "extra.txt", "v2")

	updated, err := m.UpdateAutoUpgrade(context.Background())
	if err != nil {
		t.Fatalf("UpdateAutoUpgrade: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("UpdateAutoUpgrade touched a non-autoUpgrade plugin: %+v", updated)
	}
	reg, _ := LoadRegistry(m.registryPath())
	if reg.Plugins["widget@acme"][0].InstallPath != first.InstallPath {
		t.Fatal("registry moved despite autoUpgrade being disabled")
	}

	// Now opt in — the next pass must upgrade it.
	if err := m.SetAutoUpgrade("widget", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	updated, err = m.UpdateAutoUpgrade(context.Background())
	if err != nil {
		t.Fatalf("UpdateAutoUpgrade after opt-in: %v", err)
	}
	if len(updated) != 1 || updated[0].Plugin != "widget" || updated[0].Marketplace != "acme" {
		t.Fatalf("UpdateAutoUpgrade = %+v, want one upgraded widget@acme", updated)
	}
	if updated[0].Entry.InstallPath == first.InstallPath {
		t.Fatal("upgrade did not move to a new sha-dir")
	}
	if _, err := os.Stat(first.InstallPath); err != nil {
		t.Fatal("old sha-dir was deleted; UpdateAutoUpgrade must not GC")
	}
}

func TestUpdateAutoUpgrade_NoOpNotReportedAsUpdated(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, _ := makeGitBackedMarketplace(t, "widget")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := m.SetAutoUpgrade("widget", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}

	// No upstream change: sha is unchanged, so Upgrade is a no-op and must not
	// be reported as an update (the daemon uses this list to decide whether to
	// broadcast serf/plugin/updated).
	updated, err := m.UpdateAutoUpgrade(context.Background())
	if err != nil {
		t.Fatalf("UpdateAutoUpgrade: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("UpdateAutoUpgrade reported a no-op as updated: %+v", updated)
	}
}

func TestUpdateAutoUpgrade_SkipsRelativeAndDirectorySources(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t) // "widget" is a "./plugins/widget" relative source

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := m.SetAutoUpgrade("widget", name, true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}

	updated, err := m.UpdateAutoUpgrade(context.Background())
	if err != nil {
		t.Fatalf("UpdateAutoUpgrade: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("UpdateAutoUpgrade touched a relative-source plugin: %+v", updated)
	}
}

func TestUpdateAutoUpgrade_AggregatesFailuresButKeepsGoing(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, pluginRepo := makeGitBackedMarketplace(t, "widget")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := m.SetAutoUpgrade("widget", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	advanceGitRepo(t, pluginRepo, "extra.txt", "v2")

	// Break the plugin's upstream repo so its upgrade fails; UpdateAutoUpgrade
	// must still report the error without panicking, and (with only one
	// registered plugin) simply return zero updates and a non-nil error.
	if err := os.RemoveAll(pluginRepo); err != nil {
		t.Fatal(err)
	}

	updated, err := m.UpdateAutoUpgrade(context.Background())
	if err == nil {
		t.Fatal("expected an aggregated error when the upstream repo vanished")
	}
	if len(updated) != 0 {
		t.Fatalf("updated = %+v, want none", updated)
	}
}
