package hub

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// TestCanceledDeletionFencedActionDoesNotWaitForSessionOwnership is the Medium
// regression: the deletion-fenced source lookup must acquire session ownership
// with the request's context. Acquiring it with a background context left a
// canceled or disconnected RPC parked behind a long-running explicit Resume
// instead of returning promptly.
func TestCanceledDeletionFencedActionDoesNotWaitForSessionOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		id := hubtest.SessionID(t)
		locks := hubcore.NewResumeLocks()
		blocked := locks.For(id)
		blocked.Lock()
		defer blocked.Unlock()
		cfg := hubcore.WebConfig{ResumeLocks: locks}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		go func() {
			_, err := hubJobsList(ctx, cfg, nil, appwire.JobsListParams{Ref: "local:" + id})
			completed <- err
		}()
		synctest.Wait()
		select {
		case err := <-completed:
			t.Fatalf("action returned before cancellation: %v", err)
		default:
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-completed:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled action error = %v, want context.Canceled", err)
			}
		default:
			t.Fatal("canceled RPC handler still waits for session ownership")
		}
	})
}
