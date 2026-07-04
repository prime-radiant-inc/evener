package plugins

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireLock_ExclusiveWithTimeout(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "l.lock")

	release, err := acquireLock(lp, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire must fail within the timeout while the first is held.
	_, err = acquireLock(lp, 100*time.Millisecond)
	if err == nil {
		t.Fatal("second acquire succeeded while lock held; want timeout error")
	}

	release()

	// After release, acquire must succeed again.
	release2, err := acquireLock(lp, time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}
