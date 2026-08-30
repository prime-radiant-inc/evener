package plugins

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireLock_ExclusiveWithTimeout(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "l.lock")

	release, err := acquireLock(context.Background(), lp, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire must fail within the timeout while the first is held.
	_, err = acquireLock(context.Background(), lp, 100*time.Millisecond)
	if err == nil {
		t.Fatal("second acquire succeeded while lock held; want timeout error")
	}

	release()

	// After release, acquire must succeed again.
	release2, err := acquireLock(context.Background(), lp, time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

// TestAcquireLock_ObservesContextCancellation pins that a canceled request
// stops waiting for a contended lock promptly instead of spinning out the
// full timeout (a disconnected client's handler used to park here up to 30s).
func TestAcquireLock_ObservesContextCancellation(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "l.lock")

	release, err := acquireLock(context.Background(), lp, time.Second)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = acquireLock(ctx, lp, 30*time.Second)
	if err == nil {
		t.Fatal("acquire succeeded while lock held; want cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Prompt means bounded by the backoff cap (200ms), not the 30s timeout.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("acquire took %v after cancellation; want prompt return", elapsed)
	}
}
