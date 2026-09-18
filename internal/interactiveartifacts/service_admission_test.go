package interactiveartifacts

import (
	"context"
	"sync"
	"testing"
)

func TestAdmissionNoisyPrincipalAndCancellation(t *testing.T) {
	a := newAdmission()
	key := principalKey{"realm", "noisy"}
	var releases []func()
	for i := 0; i < 4; i++ {
		release, err := a.acquire(context.Background(), key)
		requireNoError(t, err)
		releases = append(releases, release)
	}
	// Populate the actual queue under its lock, then drive admission/release.
	// The waiter channels are the production completion events, not elapsed time.
	a.mu.Lock()
	waiters := make([]*admissionWaiter, 128)
	for i := range waiters {
		waiters[i] = &admissionWaiter{key: key, ready: make(chan struct{})}
		a.queue = append(a.queue, waiters[i])
	}
	a.mu.Unlock()
	_, err := a.acquire(context.Background(), key)
	requireCode(t, err, Busy)
	unrelated, err := a.acquire(context.Background(), principalKey{"realm", "other"})
	requireNoError(t, err)
	unrelated()
	releases[0]()
	<-waiters[0].ready
	a.mu.Lock()
	active, queued := a.active, len(a.queue)
	a.mu.Unlock()
	if active != 4 || queued != 127 {
		t.Fatalf("active=%d queued=%d", active, queued)
	}
	a.close()
	for _, waiter := range waiters {
		<-waiter.ready
	}
}

func TestAdmissionCanceledCallerDoesNotReleaseAnotherLease(t *testing.T) {
	a := newAdmission()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.acquire(ctx, principalKey{"r", "p"})
	requireCode(t, err, Busy)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
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
