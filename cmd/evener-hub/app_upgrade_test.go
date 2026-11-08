package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/selfupdate"
)

func TestHubUpgradeRefusedWhileApplyHoldsTheLock(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	if !hubUpdateMu.TryLock() {
		t.Fatal("failed to take hubUpdateMu")
	}
	t.Cleanup(hubUpdateMu.Unlock)

	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		t.Fatal("upgrade seam must not be called while apply holds the lock")
		return selfupdate.Result{}, nil
	})

	_, err := hubUpgrade(context.Background(), appwire.UpgradeParams{})
	if !errors.Is(err, errHubUpdateInProgress) {
		t.Fatalf("hubUpgrade err = %v", err)
	}
	if *upgrades != 0 {
		t.Fatalf("upgrades = %d", *upgrades)
	}
}

func TestHubUpgradeInstallsForLocalBuild(t *testing.T) {
	setBuild(t, "abc1234", "")
	setManagedRestartProbe(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	var gotOpts selfupdate.Options
	upgrades := stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, "evener"), nil
	})

	got, err := hubUpgrade(context.Background(), appwire.UpgradeParams{Requested: "snapshot"})
	if err != nil {
		t.Fatalf("hubUpgrade: %v", err)
	}
	if *upgrades != 1 {
		t.Fatalf("upgrades = %d, want 1", *upgrades)
	}
	if gotOpts.Requested != "snapshot" || gotOpts.CurrentChannel != "release" {
		t.Fatalf("opts = %+v", gotOpts)
	}
	if got.Channel != "snapshot" || len(got.Installed) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestHubUpgradeLocalBuildOutsideInstallationRefused(t *testing.T) {
	setBuild(t, "abc1234", "")
	setRestartProbe(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		t.Fatal("a local build outside its install target must not install")
		return selfupdate.Result{}, nil
	})

	_, err := hubUpgrade(context.Background(), appwire.UpgradeParams{Requested: "release"})
	if !errors.Is(err, errLocalBuildInstallTarget) {
		t.Fatalf("hubUpgrade err = %v, want errLocalBuildInstallTarget", err)
	}
	if *upgrades != 0 {
		t.Fatalf("upgrades = %d, want 0", *upgrades)
	}
}

func TestHubUpdateApplyRefusedWhileUpgradeHoldsTheLock(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	if !hubUpdateMu.TryLock() {
		t.Fatal("failed to take hubUpdateMu")
	}
	t.Cleanup(hubUpdateMu.Unlock)

	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		t.Fatal("upgrade seam must not be called while an upgrade holds the lock")
		return selfupdate.Result{}, nil
	})
	restarts := stubScheduleRestart(t)

	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if !errors.Is(err, errHubUpdateInProgress) {
		t.Fatalf("hubUpdateApply err = %v", err)
	}
	if *upgrades != 0 || len(*restarts) != 0 {
		t.Fatalf("upgrades=%d restarts=%d", *upgrades, len(*restarts))
	}
}

func TestHubUpgradeReleasesTheLockAfterReturning(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return stubInstalledResult(t, "evener"), nil
	})

	if _, err := hubUpgrade(context.Background(), appwire.UpgradeParams{}); err != nil {
		t.Fatalf("hubUpgrade: %v", err)
	}

	if !hubUpdateMu.TryLock() {
		t.Fatal("hubUpdateMu still locked after hubUpgrade returned")
	}
	hubUpdateMu.Unlock()
}

func TestHubUpgradeReleasesTheLockOnError(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, context.Canceled
	})

	if _, err := hubUpgrade(context.Background(), appwire.UpgradeParams{}); err == nil {
		t.Fatal("expected error")
	}

	if !hubUpdateMu.TryLock() {
		t.Fatal("hubUpdateMu still locked after hubUpgrade returned an error")
	}
	hubUpdateMu.Unlock()
}
