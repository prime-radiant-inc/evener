package hub

import (
	"context"
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
	previousErr := hubLaunchEntrypointErr
	hubLaunchEntrypointErr = nil
	t.Cleanup(func() { hubLaunchEntrypointErr = previousErr })

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
	setRestartProbe(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")

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
	setRestartProbe(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")

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
	if resp.OnDiskCommit != "abc1234" {
		t.Fatalf("on-disk commit = %q, want abc1234", resp.OnDiskCommit)
	}
	if !resp.RestartSupported {
		t.Fatal("restartSupported = false on a usable dev build")
	}
	if resp.InstallAvailable {
		t.Fatal("installAvailable = true on a dev build")
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

func setRestartProbe(t *testing.T, body string) {
	t.Helper()
	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, body)
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := hubLaunchEntrypointErr
	hubLaunchEntrypointErr = nil
	t.Cleanup(func() { hubLaunchEntrypointErr = previousErr })
}
