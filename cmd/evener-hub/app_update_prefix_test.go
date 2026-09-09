package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/selfupdate"
)

// TestHubUpdateApplyUpgradesTheRunningPrefix proves a hub executing from a
// system prefix upgrades that prefix: the apply path derives Prefix,
// BinDir, and ShareBinDir from the running binary instead of letting
// Upgrade fall back to ~/.local. Fails today because hubUpdateApply passes
// no install dirs at all.
func TestHubUpdateApplyUpgradesTheRunningPrefix(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	previousExe := hubExecutable
	hubExecutable = func() (string, error) { return "/usr/local/share/evener/bin/evener", nil }
	t.Cleanup(func() { hubExecutable = previousExe })

	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, "evener", "evener-dev"), nil
	})
	stubScheduleRestart(t)

	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Prefix != "/usr/local" {
		t.Fatalf("Prefix = %q, want %q", gotOpts.Prefix, "/usr/local")
	}
	if gotOpts.BinDir != "/usr/local/bin" {
		t.Fatalf("BinDir = %q, want %q", gotOpts.BinDir, "/usr/local/bin")
	}
	if gotOpts.ShareBinDir != "/usr/local/share/evener/bin" {
		t.Fatalf("ShareBinDir = %q, want %q", gotOpts.ShareBinDir, "/usr/local/share/evener/bin")
	}
}

// TestHubUpdateApplyResolvesBinSymlinkEntrypoint proves the normal
// install.sh entrypoint works end to end at the apply layer: a hub launched
// as <prefix>/bin/evener (a symlink to ../share/evener/bin/evener, as PATH
// resolves it) upgrades <prefix>. os.Executable reports the symlink path
// unresolved, so without symlink resolution the derivation falls back to
// ~/.local and the wrong prefix is upgraded.
func TestHubUpdateApplyResolvesBinSymlinkEntrypoint(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	root := t.TempDir()
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "evener")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	previousExe := hubExecutable
	hubExecutable = func() (string, error) { return link, nil }
	t.Cleanup(func() { hubExecutable = previousExe })

	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, filepath.Join(shareBin, "evener")), nil
	})
	stubScheduleRestart(t)

	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Prefix != root {
		t.Fatalf("Prefix = %q, want %q", gotOpts.Prefix, root)
	}
	if gotOpts.BinDir != binDir {
		t.Fatalf("BinDir = %q, want %q", gotOpts.BinDir, binDir)
	}
	if gotOpts.ShareBinDir != shareBin {
		t.Fatalf("ShareBinDir = %q, want %q", gotOpts.ShareBinDir, shareBin)
	}
}

// TestHubUpdateApplyDefaultsInstallDirsForUnknownLayouts proves a hub
// running from a worktree build (no install layout) still upgrades with
// Upgrade's own defaults: empty install dirs, not garbage.
func TestHubUpdateApplyDefaultsInstallDirsForUnknownLayouts(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	previousExe := hubExecutable
	hubExecutable = func() (string, error) { return "/tmp/worktree-evener-build/evener-hub", nil }
	t.Cleanup(func() { hubExecutable = previousExe })

	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, "evener"), nil
	})
	stubScheduleRestart(t)

	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Prefix != "" || gotOpts.BinDir != "" || gotOpts.ShareBinDir != "" {
		t.Fatalf("opts = %+v, want empty install dirs for an unknown layout", gotOpts)
	}
}

// TestHubUpgradeUpgradesTheRunningPrefix proves evener/upgrade (the TUI
// path) gets the same treatment: without it, fixing only apply would leave
// the older RPC installing into the wrong prefix.
func TestHubUpgradeUpgradesTheRunningPrefix(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	previousExe := hubExecutable
	hubExecutable = func() (string, error) { return "/usr/local/share/evener/bin/evener", nil }
	t.Cleanup(func() { hubExecutable = previousExe })

	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, "evener"), nil
	})

	if _, err := hubUpgrade(context.Background(), appwire.UpgradeParams{}); err != nil {
		t.Fatalf("hubUpgrade: %v", err)
	}
	if gotOpts.Prefix != "/usr/local" || gotOpts.BinDir != "/usr/local/bin" || gotOpts.ShareBinDir != "/usr/local/share/evener/bin" {
		t.Fatalf("opts = %+v, want the running /usr/local layout", gotOpts)
	}
}
