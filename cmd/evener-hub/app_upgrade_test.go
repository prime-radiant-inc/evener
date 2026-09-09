package hub

import (
	"context"
	"strings"
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
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("hubUpgrade err = %v", err)
	}
	if *upgrades != 0 {
		t.Fatalf("upgrades = %d", *upgrades)
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
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
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
