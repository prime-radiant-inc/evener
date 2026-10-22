package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/selfupdate"
)

func TestHubUpdateCheckReportsOnDiskBuild(t *testing.T) {
	setBuild(t, "abc1234", "release")
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel:         opts.Channel,
			LatestTag:       "release",
			LatestCommit:    "abc1234" + strings.Repeat("f", 33),
			UpdateAvailable: true,
		}, nil
	})

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	started := time.Date(2026, 10, 21, 12, 0, 0, 42, time.UTC)
	previousStarted := hubProcessStartedAt
	hubProcessStartedAt = started
	t.Cleanup(func() { hubProcessStartedAt = previousStarted })

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if resp.OnDiskCommit != "abc1234" {
		t.Fatalf("on-disk commit = %q, want abc1234", resp.OnDiskCommit)
	}
	if resp.OnDiskError != "" {
		t.Fatalf("on-disk error = %q, want none", resp.OnDiskError)
	}
	if !resp.StartedAt.Equal(started) {
		t.Fatalf("started_at = %v, want %v", resp.StartedAt, started)
	}
	if !resp.RestartSupported {
		t.Fatal("restartSupported = false, want true")
	}
	if resp.InstallAvailable {
		t.Fatal("installAvailable = true although the on-disk build is the channel's latest")
	}
	if resp.RestartPending {
		t.Fatal("restartPending = true although the running and on-disk builds match")
	}
}

func TestHubUpdateCheckMarksInstallAvailableWhenOnDiskIsOlder(t *testing.T) {
	setBuild(t, "fffffff", "release")
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel:         opts.Channel,
			LatestTag:       "release",
			LatestCommit:    "def5678" + strings.Repeat("f", 33),
			UpdateAvailable: true,
		}, nil
	})
	setManagedRestartProbe(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if !resp.InstallAvailable {
		t.Fatal("installAvailable = false although the on-disk build is behind the channel")
	}
	if !resp.RestartPending {
		t.Fatal("restartPending = false although the running and on-disk builds differ")
	}
}

func TestHubUpdateCheckInstallsSelectedChannelOverDifferentOnDisk(t *testing.T) {
	setBuild(t, "abc1234", "release")
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel:         opts.Channel,
			LatestTag:       "release",
			LatestCommit:    "abc1234" + strings.Repeat("f", 33),
			UpdateAvailable: false,
		}, nil
	})
	setManagedRestartProbe(t, "echo 'evener def5678 (2026-10-21T00:00:00Z)'")

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if resp.UpdateAvailable {
		t.Fatal("updateAvailable = true although the running build is the channel's latest")
	}
	if !resp.InstallAvailable {
		t.Fatal("installAvailable = false although the on-disk build differs from the channel")
	}
}

func TestHubUpdateCheckReportsProbeFailure(t *testing.T) {
	setBuild(t, "abc1234", "release")
	stubUpdateAvailable(t)
	setRestartProbe(t, "echo nope; exit 1")

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if resp.OnDiskError == "" {
		t.Fatal("on-disk probe failure was not reported")
	}
	if resp.InstallAvailable {
		t.Fatal("installAvailable = true after a failed on-disk probe")
	}
	if resp.RestartSupported {
		t.Fatal("restartSupported = true after a failed on-disk probe")
	}
}

func TestHubUpdateCheckPopulatesDevFieldsBeforeEarlyReturn(t *testing.T) {
	setBuild(t, "abc1234", "dev")
	checkCalls := stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		t.Fatal("dev build reached the network check")
		return selfupdate.CheckResult{}, nil
	})
	setRestartProbe(t, "echo 'evener def5678 (2026-10-21T00:00:00Z)'")

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if *checkCalls != 0 {
		t.Fatalf("network check calls = %d, want 0", *checkCalls)
	}
	if resp.Applicable || resp.UpdateAvailable {
		t.Fatalf("dev check = applicable:%v updateAvailable:%v, want both false", resp.Applicable, resp.UpdateAvailable)
	}
	if resp.OnDiskCommit != "def5678" {
		t.Fatalf("on-disk commit = %q, want def5678", resp.OnDiskCommit)
	}
	if !resp.RestartSupported {
		t.Fatal("restartSupported = false on a usable dev build")
	}
	if resp.InstallAvailable {
		t.Fatal("installAvailable = true on a dev build")
	}
	if !resp.RestartPending {
		t.Fatal("restartPending = false although the dev build on disk differs")
	}
}

func TestHubUpdateCheckKeepsDirtyOnDiskBuildPending(t *testing.T) {
	setBuild(t, "abc1234", "release")
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel:         opts.Channel,
			LatestTag:       "release",
			LatestCommit:    "abc1234" + strings.Repeat("f", 33),
			UpdateAvailable: true,
		}, nil
	})
	setRestartProbe(t, "echo 'evener abc1234-dirty (2026-10-21T00:00:00Z)'")

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if resp.OnDiskCommit != "abc1234-dirty" {
		t.Fatalf("on-disk commit = %q, want abc1234-dirty", resp.OnDiskCommit)
	}
	if resp.InstallAvailable {
		t.Fatal("installAvailable = true although the on-disk commit is the channel's latest")
	}
	if !resp.RestartPending {
		t.Fatal("restartPending = false although the on-disk build is dirty")
	}
}

func TestHubUpdateCheckDetectsSameVersionRebuild(t *testing.T) {
	setBuild(t, "abc1234", "release")
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel:         opts.Channel,
			LatestTag:       "release",
			LatestCommit:    "ffffffffffffffffffffffffffffffffffffffff",
			UpdateAvailable: true,
		}, nil
	})
	setRestartProbe(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	pin, err := pinInstalledBinary(hubLaunchEntrypoint)
	if err != nil {
		t.Fatal(err)
	}
	previousHash := hubLaunchBinarySHA256
	hubLaunchBinarySHA256 = pin.sha256Hex
	t.Cleanup(func() { hubLaunchBinarySHA256 = previousHash })
	if err := os.WriteFile(hubLaunchEntrypoint, []byte("#!/bin/sh\necho 'evener abc1234 (2026-10-21T00:00:00Z)'\n# rebuilt\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if resp.OnDiskCommit != "abc1234" {
		t.Fatalf("on-disk commit = %q, want abc1234", resp.OnDiskCommit)
	}
	if !resp.RestartPending {
		t.Fatal("restartPending = false although the on-disk binary changed")
	}
}

func TestHubUpdateCheckKeepsLocalFieldsWhenChannelFails(t *testing.T) {
	setBuild(t, "abc1234", "release")
	stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{}, errors.New("GET x: 403 Forbidden: API rate limit exceeded")
	})
	setRestartProbe(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if resp.CheckError == "" {
		t.Fatal("check error is empty, want the channel failure")
	}
	if resp.OnDiskCommit != "abc1234" {
		t.Fatalf("on-disk commit = %q, want abc1234", resp.OnDiskCommit)
	}
	if resp.StartedAt.IsZero() {
		t.Fatal("startedAt is zero after a channel failure")
	}
	if !resp.RestartSupported {
		t.Fatal("restartSupported = false after a channel failure")
	}
	if resp.UpdateAvailable || resp.InstallAvailable {
		t.Fatalf("updateAvailable=%v installAvailable=%v, want both false", resp.UpdateAvailable, resp.InstallAvailable)
	}
}

func setRestartProbe(t *testing.T, body string) {
	t.Helper()
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, body)
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })
}

// TestHubUpdateCheckAcceptsCopiedBinEntrypoint covers an install launched
// from <prefix>/bin/evener as a copy or hardlink rather than a symlink:
// Upgrade replaces that entrypoint with a symlink to the managed binary, so
// the install still updates the launch path even though the share binary
// does not exist yet.
func TestHubUpdateCheckAcceptsCopiedBinEntrypoint(t *testing.T) {
	setBuild(t, "fffffff", "release")
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel:         opts.Channel,
			LatestTag:       "release",
			LatestCommit:    "def5678" + strings.Repeat("f", 33),
			UpdateAvailable: true,
		}, nil
	})
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(binDir, "evener")
	body := "#!/bin/sh\necho 'evener abc1234 (2026-10-21T00:00:00Z)'\n"
	if err := os.WriteFile(launch, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })
	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = launch
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })
	previousArgs := hubProcessArgs
	hubProcessArgs = func() []string { return []string{launch, "hub"} }
	t.Cleanup(func() { hubProcessArgs = previousArgs })

	resp, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	wantTarget := filepath.Join(root, "share", "evener", "bin", "evener")
	if resp.InstallTarget != wantTarget {
		t.Fatalf("install target = %q, want %q", resp.InstallTarget, wantTarget)
	}
	if !resp.InstallTargetMatchesLaunch {
		t.Fatal("install target does not match a copied bin entrypoint that Upgrade replaces")
	}
	if !resp.InstallAvailable {
		t.Fatal("installAvailable = false for a copied bin entrypoint")
	}
}
func setManagedRestartProbe(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })
	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = path
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })
	previousArgs := hubProcessArgs
	hubProcessArgs = func() []string { return []string{path, "hub"} }
	t.Cleanup(func() { hubProcessArgs = previousArgs })
	previousExe := hubExecutable
	hubExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { hubExecutable = previousExe })
	return path
}
