package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// makeGitBackedMarketplaceTwoPlugins is makeGitBackedMarketplace but with two
// independently git-backed plugins in one marketplace, so a test can break
// one while leaving the other upgradeable.
func makeGitBackedMarketplaceTwoPlugins(t *testing.T, pluginA, pluginB string) (mktRepo, pluginARepo, pluginBRepo string) {
	t.Helper()
	pluginARepo = filepath.Join(t.TempDir(), pluginA+"repo")
	writePlugin(t, pluginARepo, pluginA, nil)
	makeGitRepo(t, pluginARepo, "extra.txt", "v1")

	pluginBRepo = filepath.Join(t.TempDir(), pluginB+"repo")
	writePlugin(t, pluginBRepo, pluginB, nil)
	makeGitRepo(t, pluginBRepo, "extra.txt", "v1")

	mktRepo = filepath.Join(t.TempDir(), "mkt")
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[` +
		`{"name":"` + pluginA + `","source":{"source":"url","url":"` + pluginARepo + `"}},` +
		`{"name":"` + pluginB + `","source":{"source":"url","url":"` + pluginBRepo + `"}}` +
		`]}`
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, mktRepo, "README.md", "x")
	return mktRepo, pluginARepo, pluginBRepo
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

	// Opt back out — a plugin that WAS auto-upgrade-enabled must stop being
	// touched the moment the flag flips back off, not just when it was never
	// enabled in the first place.
	if err := m.SetAutoUpgrade("widget", "acme", false); err != nil {
		t.Fatalf("SetAutoUpgrade(false): %v", err)
	}
	advanceGitRepo(t, pluginRepo, "extra.txt", "v3")
	beforeOptOut := updated[0].Entry.InstallPath
	updated, err = m.UpdateAutoUpgrade(context.Background())
	if err != nil {
		t.Fatalf("UpdateAutoUpgrade after opt-out: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("UpdateAutoUpgrade touched a plugin after it opted back out: %+v", updated)
	}
	reg, _ = LoadRegistry(m.registryPath())
	if reg.Plugins["widget@acme"][0].InstallPath != beforeOptOut {
		t.Fatal("registry moved despite autoUpgrade having been turned back off")
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

// TestUpdateAutoUpgrade_AggregatesFailuresButKeepsGoing registers a broken
// auto-upgrade plugin AND a healthy one, sorting before it ("broken" <
// "healthy"), so the sweep hits the failure first. Asserting only "an error
// came back" would also pass if the sweep aborted on the first failure
// instead of continuing — the healthy plugin's actual upgrade is what proves
// the per-plugin `continue` really continues.
func TestUpdateAutoUpgrade_AggregatesFailuresButKeepsGoing(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, brokenRepo, healthyRepo := makeGitBackedMarketplaceTwoPlugins(t, "broken", "healthy")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "broken", "acme"); err != nil {
		t.Fatalf("Install broken: %v", err)
	}
	if err := m.SetAutoUpgrade("broken", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade broken: %v", err)
	}
	advanceGitRepo(t, brokenRepo, "extra.txt", "v2")

	healthy, err := m.Install(context.Background(), "healthy", "acme")
	if err != nil {
		t.Fatalf("Install healthy: %v", err)
	}
	if err := m.SetAutoUpgrade("healthy", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade healthy: %v", err)
	}
	advanceGitRepo(t, healthyRepo, "extra.txt", "v2")

	// Break "broken"'s upstream repo so its upgrade fails; UpdateAutoUpgrade
	// must still report the error AND go on to upgrade "healthy".
	if err := os.RemoveAll(brokenRepo); err != nil {
		t.Fatal(err)
	}

	updated, err := m.UpdateAutoUpgrade(context.Background())
	if err == nil {
		t.Fatal("expected an aggregated error when broken's upstream repo vanished")
	}
	if !strings.Contains(err.Error(), "broken@acme") {
		t.Fatalf("error = %v, want it to mention broken@acme", err)
	}
	if len(updated) != 1 || updated[0].Plugin != "healthy" || updated[0].Marketplace != "acme" {
		t.Fatalf("updated = %+v, want exactly healthy@acme upgraded despite broken's failure", updated)
	}
	if updated[0].Entry.InstallPath == healthy.InstallPath {
		t.Fatal("healthy plugin was not actually upgraded to a new sha-dir")
	}
}

// TestUpdateAutoUpgrade_ConcurrentSweepDoesNotDuplicateReport reproduces the
// TOCTOU two overlapping sweeps can hit (the periodic tick racing a manual
// serf/plugin/checkNow, or checkNow from two clients): sweep A completes the
// real upgrade while sweep B is still holding a pre-upgrade view of the
// plugin. B must not ALSO report the plugin as updated — its change
// detection has to be computed fresh, at the moment it actually acts, not
// from whatever it observed before A ran.
//
// The interleaving is forced deterministically rather than by luck: the test
// takes the manager's lock itself before starting sweep B, so B's own
// upgrade attempt (inside UpdateAutoUpgrade, gated on the same lock) cannot
// possibly proceed until the test releases it — that part has no race
// window. Sweep A is simulated with a direct, lock-holding upgradeLocked
// call standing in for "some other sweep already got there first". The one
// timing-sensitive detail is that B's unlocked "which plugins exist" read
// should happen before sweep A's write for the interleaving to be
// interesting; a short sleep biases the scheduler toward that (a plain file
// read vs. a git clone). Confirmed by temporarily reintroducing the old
// stale-snapshot comparison in UpdateAutoUpgrade: this test fails against
// that code and passes against the fix.
func TestUpdateAutoUpgrade_ConcurrentSweepDoesNotDuplicateReport(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, pluginRepo := makeGitBackedMarketplace(t, "widget")

	root := t.TempDir()
	m1 := NewManager(root)
	m2 := NewManager(root) // a second "client" contending for the same lock file

	if _, err := m1.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := m1.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := m1.SetAutoUpgrade("widget", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	advanceGitRepo(t, pluginRepo, "extra.txt", "v2")

	// Hold the lock ourselves so sweep B (started below) is guaranteed to
	// block on its own upgrade attempt until we release it.
	release, err := acquireLock(m1.lockPath(), 5*time.Second)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}

	var wg sync.WaitGroup
	var bUpdated []UpgradedPlugin
	var bErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		bUpdated, bErr = m2.UpdateAutoUpgrade(context.Background())
	}()

	// Bias the scheduler toward sweep B completing its cheap, unlocked "list
	// installed plugins" read before we perform the real upgrade below. This
	// cannot make the test flaky in the other direction: B's per-plugin work
	// is gated on the lock we hold regardless of when this fires.
	time.Sleep(20 * time.Millisecond)

	// Simulate sweep A completing the real upgrade while B is blocked.
	entry, changed, skipped, err := m1.upgradeLocked(context.Background(), "widget", "acme", false)
	if err != nil || skipped || !changed {
		t.Fatalf("setup: direct upgradeLocked changed=%v skipped=%v err=%v", changed, skipped, err)
	}
	if entry.InstallPath == first.InstallPath {
		t.Fatal("setup: direct upgrade did not move to a new sha-dir")
	}

	release()
	wg.Wait()

	if bErr != nil {
		t.Fatalf("UpdateAutoUpgrade (sweep B): %v", bErr)
	}
	if len(bUpdated) != 0 {
		t.Fatalf("sweep B reported %+v as updated; sweep A already performed this exact upgrade — duplicate report", bUpdated)
	}

	reg, _ := LoadRegistry(m1.registryPath())
	if reg.Plugins["widget@acme"][0].GitCommitSha != entry.GitCommitSha {
		t.Fatal("registry not left at the sha sweep A upgraded to")
	}
}
