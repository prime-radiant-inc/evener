package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
