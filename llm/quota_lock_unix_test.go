//go:build darwin || linux

package llm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// holdQuotaLock takes the coordinator's one guard file and returns it held, so
// Wait cannot acquire the lock until the test releases it.
func holdQuotaLock(t *testing.T, dir string) (*os.File, string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.lock"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("quota lock files in %s = %v (err %v), want exactly one", dir, paths, err)
	}
	f, err := os.OpenFile(paths[0], os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open quota lock %s: %v", paths[0], err)
	}
	if err := lockQuotaFile(context.Background(), f); err != nil {
		_ = f.Close()
		t.Fatalf("lock %s: %v", paths[0], err)
	}
	return f, paths[0]
}

func releaseQuotaLock(f *os.File) {
	_ = unlockQuotaFile(f)
	_ = f.Close()
}

// A lock holder that is suspended must not hold a serve turn past the retry
// budget: reportAndEnqueue's lock acquisition is bounded by Budget, and the
// only remaining work is the caller's own fallback cooldown.
func TestQuotaCoordinator_WaitBoundsLockAcquisitionByBudget(t *testing.T) {
	clk := newBudgetClock()
	dir := t.TempDir()
	q := quotaTestCoordinator(dir, clk)
	const key = "bedrock"
	seedQuotaCooldown(t, q, key, 0)

	held, lockPath := holdQuotaLock(t, dir)
	defer releaseQuotaLock(held)

	done := make(chan error, 1)
	go func() {
		done <- q.Wait(context.Background(), QuotaWait{
			Key:      key,
			Cooldown: time.Second,
			Budget:   100 * time.Millisecond,
		}, clk.sleep)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait = %v, want the best-effort fallback after the lock budget expired", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Wait blocked on the suspended lock holder at %s past its 100ms budget", lockPath)
	}
}
