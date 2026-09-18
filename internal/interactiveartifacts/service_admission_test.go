package interactiveartifacts

import (
	"context"
	"sync"
	"testing"
)

func TestAdmissionCanceledCallerDoesNotReleaseAnotherLease(t *testing.T) {
	a := newAdmission()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.acquire(ctx, principalKey{"r", "p"})
	requireCode(t, err, Busy)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			release, err := a.acquire(context.Background(), principalKey{"r", "p"})
			if err != nil {
				t.Error(err)
				return
			}
			release()
			release()
		})
	}
	wg.Wait()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != 0 || len(a.queue) != 0 || len(a.principals) != 0 || a.peakActive > 4 {
		t.Fatalf("admission leaked: %+v", a)
	}
}
