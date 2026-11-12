package hub

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// blockingNavigationSource blocks its capture until the build context ends,
// then serves stable snapshots. It reproduces a forced receipt refresh that
// times out and a later recovery capture that can commit the same change.
type blockingNavigationSource struct {
	*testNavigationSource
	block atomic.Bool
}

func (s *blockingNavigationSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	if s.block.Load() {
		<-ctx.Done()
		return navigationSourceSnapshot{}, ctx.Err()
	}
	return s.testNavigationSource.Capture(ctx, generation, now)
}

// A forced refresh tells a mutation handler whether a deadline is deferrable
// (a rebuild is armed to publish the receipt) or permanent (no later receipt
// can arrive). The deferred claim is true only when a rebuild is actually
// pending, and permanent failures stay errors.
func TestNavigationRefreshDistinguishesDeferredDeadlineFromPermanentFailure(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	t.Run("deadline with an armed rebuild defers", func(t *testing.T) {
		service := newChurningNavigationService(t)
		service.Invalidate(navigationChangeHint{AllLoadedProjects: true})

		mutation, err := service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
		if err != nil {
			t.Fatalf("refresh with an armed rebuild failed: %v", err)
		}
		assertDeferredNavigationReceipt(t, service, mutation)
		if _, pending := service.pendingAttemptAt(); !pending {
			t.Fatal("deferred receipt consumed the invalidation whose rebuild it claims is pending")
		}
	})

	t.Run("deadline without an armed rebuild stays an error", func(t *testing.T) {
		service := newChurningNavigationService(t)

		mutation, err := service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want the deadline", err)
		}
		var status interface{ StatusCode() int }
		if !errors.As(err, &status) || status.StatusCode() != 503 {
			t.Fatalf("error = %T %v, want a typed 503", err, err)
		}
		if mutation.Deferred {
			t.Fatalf("mutation = %+v, want no deferred claim without a pending rebuild", mutation)
		}
	})

	t.Run("permanent failures stay errors", func(t *testing.T) {
		generation := newTestNavigationService(t, newTestNavigationSource(now), func(cfg *navigationServiceConfig) {
			cfg.Generation = func() (string, error) { return "", errors.New("entropy unavailable") }
		})
		noSource := newNavigationService(navigationServiceConfig{
			Generation: func() (string, error) { return "00112233445566778899aabbccddeeff", nil },
		})
		sequence := newTestNavigationService(t, newTestNavigationSource(now))
		sequence.mu.Lock()
		sequence.sequence = maxNavigationSafeInteger
		sequence.mu.Unlock()

		for _, test := range []struct {
			name    string
			service *NavigationService
		}{
			{name: "generation failure", service: generation},
			{name: "missing source", service: noSource},
			{name: "sequence exhausted", service: sequence},
		} {
			t.Run(test.name, func(t *testing.T) {
				mutation, err := test.service.Refresh(t.Context(), navigationChangeHint{})
				if err == nil {
					t.Fatalf("refresh succeeded, want a permanent failure")
				}
				if mutation.Deferred {
					t.Fatalf("mutation = %+v, want no deferred claim for a permanent failure", mutation)
				}
			})
		}
	})
}

// A forced receipt refresh can time out while its source change is still
// uncommitted. The scheduler's next successful capture must publish that
// change, not advance the fingerprints silently and let the later forced
// refresh clear the invalidation without a publication.
func TestNavigationRecoveryCapturePublishesAPendingInvalidationAfterATimedOutRefresh(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := &blockingNavigationSource{testNavigationSource: newTestNavigationSource(now)}
	service := newTestNavigationService(t, source.testNavigationSource, func(cfg *navigationServiceConfig) {
		cfg.Source = source
		cfg.BuildTimeout = 20 * time.Millisecond
	})
	if _, err := service.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}, nil); err != nil {
		t.Fatal(err)
	}

	// The store change arms the rebuild before the handler's forced refresh.
	service.Invalidate(navigationChangeHint{AllLoadedProjects: true})
	source.changeTitle("favorited")
	source.block.Store(true)
	mutation, err := service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
	if err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	if !mutation.Deferred {
		t.Fatalf("mutation = %+v, want a deferred receipt", mutation)
	}

	// The source stabilizes. Start's non-forced recovery capture is the first
	// build that can commit the change; it must publish the invalidation.
	source.block.Store(false)
	flight, err := service.ensureSnapshot(t.Context(), false, nil)
	if err != nil {
		t.Fatalf("recovery capture: %v", err)
	}
	if flight.err != nil {
		t.Fatalf("recovery capture error: %v", flight.err)
	}
	publications := service.DrainPublications()
	if len(publications) != 1 {
		t.Fatalf("publications = %+v, want one recovery publication", publications)
	}
	if !hasNavigationTarget(publications[0].Targets, appwire.NavigationTargetProject, "p1") {
		t.Fatalf("recovery targets = %+v, want project p1", publications[0].Targets)
	}
	if targets := publications[0].Targets; len(targets) == 0 || targets[len(targets)-1].Kind != appwire.NavigationTargetAllLoadedProjects {
		t.Fatalf("recovery targets = %+v, want the all-loaded-projects wildcard", publications[0].Targets)
	}
	if got := service.Capability().Sequence; got != 1 {
		t.Fatalf("sequence = %d, want one recovery publication", got)
	}
	if _, pending := service.pendingAttemptAt(); pending {
		t.Fatal("recovery capture left the invalidation it published armed for a second publication")
	}
}
