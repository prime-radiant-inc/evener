package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/selfupdate"
)

// TestHubUpdateApplyPrefersInvocationPath proves the derivation uses the
// INVOCATION path (argv[0]) first: on Linux os.Executable pre-resolves
// /proc/self/exe to the share target, making a custom-BINDIR symlink
// invisible, while argv[0] still names the symlink the user launched.
// Fails today: hubInstallDirs reads only hubExecutable().
func TestHubUpdateApplyPrefersInvocationPath(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	customBin := filepath.Join(t.TempDir(), "custom-bin")
	if err := os.MkdirAll(customBin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(customBin, "evener")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	previousExe, previousArgs := hubExecutable, hubProcessArgs
	// Simulate Linux: Executable pre-resolved, argv[0] still the symlink.
	hubExecutable = func() (string, error) { return target, nil }
	hubProcessArgs = func() []string { return []string{link, "hub"} }
	t.Cleanup(func() { hubExecutable, hubProcessArgs = previousExe, previousArgs })

	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, filepath.Join(shareBin, "evener")), nil
	})
	stubScheduleRestart(t)

	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Prefix != resolvedRoot {
		t.Fatalf("Prefix = %q, want %q", gotOpts.Prefix, resolvedRoot)
	}
	if gotOpts.BinDir != customBin {
		t.Fatalf("BinDir = %q, want the invocation dir %q", gotOpts.BinDir, customBin)
	}
	if gotOpts.ShareBinDir != filepath.Join(resolvedRoot, "share", "evener", "bin") {
		t.Fatalf("ShareBinDir = %q, want %q", gotOpts.ShareBinDir, filepath.Join(resolvedRoot, "share", "evener", "bin"))
	}
}

// TestHubUpdateApplyResolvesBarePathInvocation proves a hub launched as
// a bare name via PATH (`evener hub`, argv[0]=="evener") still derives the
// real entrypoint: hubInstallDirs resolves through exec.LookPath before
// derivation. Fails today: the bare name goes to EvalSymlinks
// cwd-relative, misses, and falls back to Executable (pre-resolved share
// target on Linux), losing the custom BINDIR.
func TestHubUpdateApplyResolvesBarePathInvocation(t *testing.T) {
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
	customBin := filepath.Join(t.TempDir(), "custom-bin")
	if err := os.MkdirAll(customBin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(customBin, "evener")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	previousExe, previousArgs := hubExecutable, hubProcessArgs
	hubExecutable = func() (string, error) { return target, nil }
	hubProcessArgs = func() []string { return []string{"evener", "hub"} }
	t.Cleanup(func() { hubExecutable, hubProcessArgs = previousExe, previousArgs })
	t.Setenv("PATH", customBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, filepath.Join(shareBin, "evener")), nil
	})
	stubScheduleRestart(t)

	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.BinDir != customBin {
		t.Fatalf("BinDir = %q, want PATH-resolved entrypoint %q", gotOpts.BinDir, customBin)
	}
}

// TestHubRestartArgsEmptyWhenNoArgs proves the restart never panics when
// the process was launched with no argv beyond (or an empty) argv[0]:
// hubProcessArgs()[1:] on an empty slice panics. Fails today at
// app_update.go's scheduleHubRestart call site.
func TestHubRestartArgsEmptyWhenNoArgs(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"/x/evener"}} {
		if got := hubRestartArgs(args); len(got) != 0 {
			t.Fatalf("hubRestartArgs(%v) = %v, want empty", args, got)
		}
	}
	if got := hubRestartArgs([]string{"/x/evener", "hub", "-addr", "x"}); len(got) != 3 || got[0] != "hub" {
		t.Fatalf("hubRestartArgs = %v, want [hub -addr x]", got)
	}
}

// TestHubRestartPreservesEntrypoint proves the restart execs the original
// argv[0] entrypoint when it resolves to the newly installed binary: a
// custom-BINDIR hub keeps launching through its configured symlink, so
// the NEXT update still derives the custom dir. Falls back to the
// installed path when argv is empty or points elsewhere.
func TestHubRestartPreservesEntrypoint(t *testing.T) {
	root := t.TempDir()
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := t.TempDir()
	realRoot := filepath.Join(aliasParent, "root-alias")
	if err := os.Symlink(root, realRoot); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(realRoot, "share", "evener", "bin", "evener")
	if err := os.WriteFile(filepath.Join(shareBin, "evener"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	customBin := filepath.Join(t.TempDir(), "custom-bin")
	if err := os.MkdirAll(customBin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(customBin, "evener")
	if err := os.Symlink(filepath.Join(shareBin, "evener"), link); err != nil {
		t.Fatal(err)
	}

	previousArgs := hubProcessArgs
	t.Cleanup(func() { hubProcessArgs = previousArgs })

	hubProcessArgs = func() []string { return []string{link, "hub"} }
	if got := restartBinary(installed); got != link {
		t.Fatalf("restartBinary = %q, want the entrypoint %q", got, link)
	}
	hubProcessArgs = func() []string { return []string{"/elsewhere/evener", "hub"} }
	if got := restartBinary(installed); got != installed {
		t.Fatalf("restartBinary = %q, want installed fallback %q", got, installed)
	}
	hubProcessArgs = func() []string { return nil }
	if got := restartBinary(installed); got != installed {
		t.Fatalf("restartBinary = %q, want installed fallback %q", got, installed)
	}
}

// TestHubRestartAbortsWhenBinarySwapped proves the restart verifies the
// installed binary is still the one Upgrade wrote: a concurrent installer
// swapping the path in the lock-release-to-exec window must abort the
// restart (loud log, lock released, old hub keeps running) rather than
// exec a different release than the response reported. Fails today: the
// exec path carries no identity to check.
func TestHubRestartAbortsWhenBinarySwapped(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "evener")
	if err := os.WriteFile(installed, []byte("our release"), 0o755); err != nil {
		t.Fatal(err)
	}
	pinned, err := pinInstalledBinary(installed)
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	// Concurrent installer swaps the path.
	if err := os.WriteFile(installed, []byte("stranger release"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyPinnedBinary(installed, pinned); err == nil {
		t.Fatal("expected a mismatch error after the binary was swapped")
	}
	if err := os.WriteFile(installed, []byte("our release"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Same bytes, but rewrite... content hash matches again only if the
	// swap wrote identical bytes; use a fresh pin for the positive case.
	pinned2, err := pinInstalledBinary(installed)
	if err != nil {
		t.Fatalf("pin2: %v", err)
	}
	if err := verifyPinnedBinary(installed, pinned2); err != nil {
		t.Fatalf("unswapped binary rejected: %v", err)
	}
}
