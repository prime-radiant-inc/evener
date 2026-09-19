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

func TestAdmissionGlobalCapIncludesDistinctPrincipals(t *testing.T) {
	a := newAdmission()
	var releases []func()
	for i := range 32 {
		release, err := a.acquire(context.Background(), principalKey{"realm", string(rune('a' + i))})
		requireNoError(t, err)
		releases = append(releases, release)
	}
	queued := make(chan struct{}, 1)
	a.onChange = func(stats AdmissionStats) {
		if stats.Queued == 1 {
			queued <- struct{}{}
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := a.acquire(canceled, principalKey{"realm", "extra"}); done <- err }()
	<-queued
	cancel()
	requireCode(t, <-done, Busy)
	for _, release := range releases {
		release()
	}
	stats := a.stats()
	if stats.PeakActive != 32 || stats.Active != 0 || stats.Queued != 0 {
		t.Fatalf("global active bound: %+v", stats)
	}
}

func TestAuthenticatedIngressReservesRoomForOtherPrincipals(t *testing.T) {
	a := newAdmission()
	key := principalKey{"realm", "noisy"}
	var releases []func()
	for range 132 {
		release, err := a.reserveIngress(key)
		requireNoError(t, err)
		releases = append(releases, release)
	}
	_, err := a.reserveIngress(key)
	requireCode(t, err, Busy)
	other, err := a.reserveIngress(principalKey{"realm", "other"})
	requireNoError(t, err)
	other()
	for _, release := range releases {
		release()
		release()
	}
	if a.stats().InFlight != 0 {
		t.Fatal("body/queue ingress lease leaked")
	}
}
