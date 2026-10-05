package hub

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
)

func hubTestGitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// hubTestMakeGitRepo initializes a git repo at dir containing one file.
func hubTestMakeGitRepo(t *testing.T, dir, file, content string) {
	t.Helper()
	if !hubTestGitAvailable() {
		t.Skip("git not available")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
}

// hubTestAdvanceGitRepo commits a change to file in dir, advancing HEAD.
func hubTestAdvanceGitRepo(t *testing.T, dir, file, content string) {
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

func hubTestWritePlugin(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"`+name+`","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// autoUpgradeFixture builds an isolated Manager with one marketplace ("acme")
// whose one plugin ("widget") is backed by its own git repo (pluginRepo) and
// has autoUpgrade already enabled — the shape the daemon's tick acts on.
func autoUpgradeFixture(t *testing.T) (mgr *plugins.Manager, pluginRepo string, firstInstallPath string) {
	t.Helper()
	if !hubTestGitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo = filepath.Join(t.TempDir(), "pluginrepo")
	hubTestWritePlugin(t, pluginRepo, "widget")
	hubTestMakeGitRepo(t, pluginRepo, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":{"source":"url","url":"` + pluginRepo + `"}}]}`
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	hubTestMakeGitRepo(t, mktRepo, "README.md", "x")

	mgr = plugins.NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := mgr.AddMarketplace(ctx, "", plugins.Source{Kind: plugins.SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := mgr.Install(ctx, "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := mgr.SetAutoUpgrade(context.Background(), "widget", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	return mgr, pluginRepo, first.InstallPath
}

// TestRunPluginAutoUpgradeTick_UpgradesAutoUpgradeEnabledPlugin is the core
// daemon test: advancing the upstream plugin repo's HEAD and running exactly
// one tick (no ticker, no goroutine) must refresh the marketplace, upgrade the
// autoUpgrade-enabled plugin to the new sha, and report it as updated.
func TestRunPluginAutoUpgradeTick_UpgradesAutoUpgradeEnabledPlugin(t *testing.T) {
	t.Parallel()
	mgr, pluginRepo, firstInstallPath := autoUpgradeFixture(t)

	hubTestAdvanceGitRepo(t, pluginRepo, "extra.txt", "v2")

	var stderr bytes.Buffer
	updated, errs := runPluginAutoUpgradeTick(context.Background(), mgr, &stderr)
	if len(errs) != 0 {
		t.Fatalf("runPluginAutoUpgradeTick errs = %v, stderr=%s", errs, stderr.String())
	}
	if len(updated) != 1 || updated[0].Plugin != "widget" || updated[0].Marketplace != "acme" {
		t.Fatalf("runPluginAutoUpgradeTick updated = %+v, want one widget@acme", updated)
	}
	if updated[0].Entry.InstallPath == firstInstallPath {
		t.Fatal("tick did not move the plugin to a new sha-dir")
	}
	if _, err := os.Stat(firstInstallPath); err != nil {
		t.Fatal("old sha-dir was deleted; the daemon must never delete")
	}

	items, err := mgr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].InstallPath != updated[0].Entry.InstallPath {
		t.Fatalf("registry not repointed to the new dir: %+v", items)
	}
}

// TestRunPluginAutoUpgradeTick_NoOpWhenUpstreamUnchanged confirms a tick with
// nothing new upstream reports zero updates (the daemon must not broadcast
// evener/plugin/updated on a no-op check).
func TestRunPluginAutoUpgradeTick_NoOpWhenUpstreamUnchanged(t *testing.T) {
	t.Parallel()
	mgr, _, _ := autoUpgradeFixture(t)

	var stderr bytes.Buffer
	updated, errs := runPluginAutoUpgradeTick(context.Background(), mgr, &stderr)
	if len(errs) != 0 {
		t.Fatalf("runPluginAutoUpgradeTick errs = %v", errs)
	}
	if len(updated) != 0 {
		t.Fatalf("runPluginAutoUpgradeTick updated = %+v, want none (upstream unchanged)", updated)
	}
}

// TestRunPluginAutoUpgradeTick_NoMarketplacesIsNoOp exercises the empty-store
// path with no git dependency: a fresh manager with nothing registered yet.
func TestRunPluginAutoUpgradeTick_NoMarketplacesIsNoOp(t *testing.T) {
	mgr := plugins.NewManager(t.TempDir())
	var stderr bytes.Buffer
	updated, errs := runPluginAutoUpgradeTick(context.Background(), mgr, &stderr)
	if len(errs) != 0 {
		t.Fatalf("runPluginAutoUpgradeTick errs = %v", errs)
	}
	if len(updated) != 0 {
		t.Fatalf("runPluginAutoUpgradeTick updated = %+v, want none", updated)
	}
}

// TestRegisterPluginAutoUpgradeHandlers_CheckNowRunsOneTick dispatches
// evener/plugin/checkNow directly through the router (bypassing the WS
// transport) against an isolated Manager, verifying the RPC wiring runs the
// same tick logic and reports the upgraded ref.
func TestRegisterPluginAutoUpgradeHandlers_CheckNowRunsOneTick(t *testing.T) {
	t.Parallel()
	mgr, pluginRepo, _ := autoUpgradeFixture(t)
	hubTestAdvanceGitRepo(t, pluginRepo, "extra.txt", "v2")

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerPluginAutoUpgradeHandlers(server, mgr)

	resp, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerPluginCheckNow,
	})
	if err != nil {
		t.Fatalf("dispatch checkNow: %v", err)
	}
	result, ok := resp.(appwire.PluginCheckNowResponse)
	if !ok {
		t.Fatalf("dispatch checkNow returned %T, want PluginCheckNowResponse", resp)
	}
	if len(result.Updated) != 1 || result.Updated[0] != "widget@acme" {
		t.Fatalf("checkNow Updated = %v, want [widget@acme]", result.Updated)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("checkNow Errors = %v, want none", result.Errors)
	}
}

func TestPlugins_CheckUpdatesFlagsAPluginWhoseRemoteMovedUntilItIsUpgraded(t *testing.T) {
	mgr, pluginRepo, _ := autoUpgradeFixture(t)
	ctl := &hubPluginsController{mgr: mgr}
	ctx := context.Background()
	ref := appwire.PluginRefParams{Plugin: "widget", Marketplace: "acme"}
	flagged := func(resp appwire.PluginListResponse, err error) bool {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Plugins) != 1 {
			t.Fatalf("plugins = %+v, want widget alone", resp.Plugins)
		}
		return resp.Plugins[0].UpdateAvailable
	}

	if flagged(ctl.CheckUpdates(ctx)) {
		t.Fatal("plugin at its remote head flagged")
	}
	hubTestAdvanceGitRepo(t, pluginRepo, "extra.txt", "v2")
	if flagged(ctl.ListPlugins(ctx)) {
		t.Fatal("plugin/list flagged a moved remote without a check")
	}
	if !flagged(ctl.CheckUpdates(ctx)) {
		t.Fatal("checkUpdates did not flag a plugin behind its remote")
	}
	if !flagged(ctl.ListPlugins(ctx)) {
		t.Fatal("plugin/list lost the flag checkUpdates found")
	}
	if flagged(ctl.Upgrade(ctx, ref)) {
		t.Fatal("upgraded plugin still flagged")
	}
}

// A check answers with the best list it can: a plugin whose remote moved is
// flagged, while one whose remote can't be reached and one with a relative
// source carry no flag, and neither fails the check.
func TestPlugins_CheckUpdatesFlagsOnlyWhatItCouldConfirm(t *testing.T) {
	if !hubTestGitAvailable() {
		t.Skip("git not available")
	}
	moved := filepath.Join(t.TempDir(), "moved")
	hubTestWritePlugin(t, moved, "moved")
	hubTestMakeGitRepo(t, moved, "extra.txt", "v1")
	unreachable := filepath.Join(t.TempDir(), "unreachable")
	hubTestWritePlugin(t, unreachable, "unreachable")
	hubTestMakeGitRepo(t, unreachable, "extra.txt", "v1")

	mktRepo := filepath.Join(t.TempDir(), "mkt")
	hubTestWritePlugin(t, filepath.Join(mktRepo, "plugins", "relative"), "relative")
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[` +
		`{"name":"moved","source":{"source":"url","url":"` + moved + `"}},` +
		`{"name":"unreachable","source":{"source":"url","url":"` + unreachable + `"}},` +
		`{"name":"relative","source":"./plugins/relative"}]}`
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	hubTestMakeGitRepo(t, mktRepo, "README.md", "x")

	mgr := plugins.NewManager(t.TempDir())
	mgr.Stderr = io.Discard
	ctx := context.Background()
	if _, err := mgr.AddMarketplace(ctx, "", plugins.Source{Kind: plugins.SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	for _, name := range []string{"moved", "unreachable", "relative"} {
		if _, err := mgr.Install(ctx, name, "acme"); err != nil {
			t.Fatalf("Install %s: %v", name, err)
		}
	}
	hubTestAdvanceGitRepo(t, moved, "extra.txt", "v2")
	if err := os.Rename(unreachable, unreachable+".gone"); err != nil {
		t.Fatal(err)
	}

	resp, err := (&hubPluginsController{mgr: mgr}).CheckUpdates(ctx)
	if err != nil {
		t.Fatalf("CheckUpdates failed on an unreachable remote: %v", err)
	}
	flagged := map[string]bool{}
	for _, entry := range resp.Plugins {
		flagged[entry.Plugin] = entry.UpdateAvailable
	}
	want := map[string]bool{"moved": true, "unreachable": false, "relative": false}
	if len(flagged) != len(want) {
		t.Fatalf("listed %v, want all three plugins", flagged)
	}
	for name, w := range want {
		if flagged[name] != w {
			t.Errorf("%s updateAvailable = %v, want %v", name, flagged[name], w)
		}
	}
}
