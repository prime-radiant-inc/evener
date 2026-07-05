package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/plugins"
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
	if err := mgr.SetAutoUpgrade("widget", "acme", true); err != nil {
		t.Fatalf("SetAutoUpgrade: %v", err)
	}
	return mgr, pluginRepo, first.InstallPath
}

// TestRunPluginAutoUpgradeTick_UpgradesAutoUpgradeEnabledPlugin is the core
// daemon test: advancing the upstream plugin repo's HEAD and running exactly
// one tick (no ticker, no goroutine) must refresh the marketplace, upgrade the
// autoUpgrade-enabled plugin to the new sha, and report it as updated.
func TestRunPluginAutoUpgradeTick_UpgradesAutoUpgradeEnabledPlugin(t *testing.T) {
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

	items, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].InstallPath != updated[0].Entry.InstallPath {
		t.Fatalf("registry not repointed to the new dir: %+v", items)
	}
}

// TestRunPluginAutoUpgradeTick_NoOpWhenUpstreamUnchanged confirms a tick with
// nothing new upstream reports zero updates (the daemon must not broadcast
// serf/plugin/updated on a no-op check).
func TestRunPluginAutoUpgradeTick_NoOpWhenUpstreamUnchanged(t *testing.T) {
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
// serf/plugin/checkNow directly through the router (bypassing the WS
// transport) against an isolated Manager, verifying the RPC wiring runs the
// same tick logic and reports the upgraded ref.
func TestRegisterPluginAutoUpgradeHandlers_CheckNowRunsOneTick(t *testing.T) {
	mgr, pluginRepo, _ := autoUpgradeFixture(t)
	hubTestAdvanceGitRepo(t, pluginRepo, "extra.txt", "v2")

	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerPluginAutoUpgradeHandlers(server, mgr)

	resp, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodSerfPluginCheckNow,
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
