package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/selfupdate"
)

// TestHubUpdateApplyBoundsTheWholeUpgrade proves the apply path enforces
// one overall deadline for the complete operation (archive download +
// checksums download + verify + install), not per-request timeouts that
// add up: the stub blocks until the passed context expires, and the apply
// must fail within the overall bound. Fails today because hubUpdateApply
// passes the caller's unbounded context straight through.
func TestHubUpdateApplyBoundsTheWholeUpgrade(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	previous := hubUpgradeTimeout
	hubUpgradeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { hubUpgradeTimeout = previous })

	stubHubSelfUpgrade(t, func(ctx context.Context, _ selfupdate.Options) (selfupdate.Result, error) {
		<-ctx.Done()
		return selfupdate.Result{}, ctx.Err()
	})
	stubScheduleRestart(t)

	start := time.Now()
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error from a stuck upgrade")
	}
	if elapsed > 30*time.Second {
		t.Fatalf("apply took %v with a 200ms overall bound; the operation has no overall deadline", elapsed)
	}
}

// TestHubUpgradeBoundsTheWholeUpgrade proves evener/upgrade (the TUI path)
// shares the same overall deadline: it funnels through the same helper.
func TestHubUpgradeBoundsTheWholeUpgrade(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	previous := hubUpgradeTimeout
	hubUpgradeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { hubUpgradeTimeout = previous })

	stubHubSelfUpgrade(t, func(ctx context.Context, _ selfupdate.Options) (selfupdate.Result, error) {
		<-ctx.Done()
		return selfupdate.Result{}, ctx.Err()
	})

	start := time.Now()
	_, err := hubUpgrade(context.Background(), appwire.UpgradeParams{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a deadline error from a stuck upgrade")
	}
	if elapsed > 30*time.Second {
		t.Fatalf("upgrade took %v with a 200ms overall bound; the operation has no overall deadline", elapsed)
	}
}

// TestHubUpgradeTimeoutDefaultClearsServerBound pins the shipped default:
// two back-to-back 5-minute per-request timeouts plus verify/install must
// fit inside it, and the frontend's APPLY_TIMEOUT_MS (6min, asserted in
// hubUpdate.test.ts) must clear it in turn -- otherwise a valid update
// outlives the page's wait and strands the UI after the hub restarts.
func TestHubUpgradeTimeoutDefaultClearsServerBound(t *testing.T) {
	if hubUpgradeTimeout <= 0 {
		t.Fatalf("hubUpgradeTimeout = %v, want a positive overall deadline", hubUpgradeTimeout)
	}
	if hubUpgradeTimeout >= 6*time.Minute {
		t.Fatalf("hubUpgradeTimeout = %v, want it under the frontend APPLY_TIMEOUT_MS (6min)", hubUpgradeTimeout)
	}
}
