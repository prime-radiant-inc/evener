package hub

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/selfupdate"
)

// TestHubUpdateApplyRefusesUnsupportedRestart proves apply fails fast when
// the platform cannot exec-in-place: today hubUpdateApply returns
// Restarting:true unconditionally, and on !unix the post-response goroutine
// fails in execHubBinary (= selfupdate.Restart, which always errors there)
// while the RPC already reported success -- so the frontend polls 30s and
// times out on an old hub that keeps running. Fails today: no platform
// guard exists.
func TestHubUpdateApplyRefusesUnsupportedRestart(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	previous := hubRestartSupported
	hubRestartSupported = func() bool { return false }
	t.Cleanup(func() { hubRestartSupported = previous })

	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, nil
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v, want an unsupported-platform refusal", err)
	}
	if *upgrades != 0 || len(*restarts) != 0 {
		t.Fatalf("upgrades=%d restarts=%d, want no download and no restart scheduled", *upgrades, len(*restarts))
	}
}

// TestHubUpdateApplyReturnsEarlyWhenUpToDate proves the server re-checks
// before installing: a direct evener/update/apply when the channel is
// already current must not download, reinstall, or exec — the frontend
// poll keys on a version change, so a no-op reinstall into an identical
// version would end in a misleading restart-timeout. Fails today: apply
// has no server-side guard (only the store refuses).
func TestHubUpdateApplyReturnsEarlyWhenUpToDate(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel: "snapshot", LatestTag: "snapshot",
			LatestCommit:    "3b1c5f8ffffffff",
			UpdateAvailable: false,
		}, nil
	})
	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, nil
	})
	restarts := stubScheduleRestart(t)
	got, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"})
	if err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if got.Restarting {
		t.Fatalf("resp = %+v, want Restarting:false for an up-to-date channel", got)
	}
	if *upgrades != 0 || len(*restarts) != 0 {
		t.Fatalf("upgrades=%d restarts=%d, want no download and no restart scheduled", *upgrades, len(*restarts))
	}
}
