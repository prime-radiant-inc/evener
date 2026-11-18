package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/internal/testwait"
)

// A close-driven wake must not park on the receiver's attention lock. An
// in-flight environment append takes attentionMu across its own publish, so a
// send it has blocked on the full authoritative stream holds that lock until the
// cascade deadline. Bounding the wake's warning does not release this earlier
// wait: reconciliation calls the arm synchronously before it returns any plans.
//
// The acknowledgment already committed and its attention is durable, so a wake
// that cannot take the lock in time loses nothing a restart cannot recover. It
// must therefore skip the arm and say so, not wait out the close.
func TestCloseDrivenWakeDoesNotWaitOutTheHeldAttentionLock(t *testing.T) {
	s := newTestSessionForEnvctx(t)

	// A goroutine holds the lock the way a blocked environment append does, and
	// releases only when the test is done with it.
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		s.attentionMu.Lock()
		close(held)
		<-release
		s.attentionMu.Unlock()
	}()
	t.Cleanup(func() {
		close(release)
	})
	testwait.Until(t, "the environment append to take the attention lock", func() bool {
		select {
		case <-held:
			return true
		default:
			return false
		}
	})

	// A bound the drain supplied, already spent.
	bound, cancelBound := context.WithCancel(t.Context())
	cancelBound()

	done := make(chan error, 1)
	go func() {
		done <- s.armDelegateAttentionsBounded(bound, []string{"delegate:held-lock"})
	}()

	// It must report the spent bound rather than park behind the held lock. The
	// wait's own deadline is the signal: the bound is already spent, so a
	// correct arm returns on its first lock attempt and testwait fails the test
	// if it does not.
	var armErr error
	testwait.Until(t, "the close-driven wake to release on its spent bound", func() bool {
		select {
		case armErr = <-done:
			return true
		default:
			return false
		}
	})
	if !errors.Is(armErr, errDelegateAttentionArmSpent) {
		t.Fatalf("arm returned %v, want the spent-bound refusal: the wake did not try to acquire under its bound", armErr)
	}
}
