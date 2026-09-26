//go:build evenerfuzz

package agent

import (
	"testing"
	"time"
)

// FuzzWvQuietWatchdogTick drives the stable delegate controller's quiet
// attention admission over adversarial elapsed times and latch state.
//
// Oracles (beyond never-panic):
//   - identical controller states make the same admission decision;
//   - quiet attention is admitted only at or beyond the inclusive window;
//   - a notified stretch re-arms: after a committed first wake, it admits the
//     repeat only once one more full window of continued silence has elapsed;
//   - an outstanding claim suppresses duplicates; and
//   - aborting persistence re-arms the same durable attention identity.
func FuzzWvQuietWatchdogTick(f *testing.F) {
	f.Add(int64(0), false)
	f.Add(int64(599), false)
	f.Add(int64(600), false)
	f.Add(int64(601), false)
	f.Add(int64(600), true)
	f.Add(int64(-1), false)

	f.Fuzz(func(t *testing.T, elapsedSec int64, alreadyNotified bool) {
		elapsed := time.Duration(elapsedSec%int64((24*time.Hour)/time.Second)) * time.Second
		begin := func() (*delegateQuietAttentionClaim, *delegateTreeController, *Session, delegateLease) {
			root, controller, lease, clock := newStableQuietSupervisionHarness(t)
			// The notified case must reach the repeat state the way production
			// does: admit and commit one wake at the first window boundary. That
			// sets quietNotified, the quietNotifiedAt baseline, and advances
			// quietSequence to 2, so the fuzzed elapsed time below measures the
			// repeat window from the first wake rather than from the activity.
			if alreadyNotified {
				seedAt := clock.Now().Add(delegateQuietWindow)
				seed, err := controller.BeginQuietAttention(root, lease, seedAt)
				if err != nil {
					t.Fatalf("seed notified claim: %v", err)
				}
				if seed == nil {
					t.Fatal("seed notified claim was not admitted at the window boundary")
				}
				if err := controller.CompleteQuietAttention(seed, true); err != nil {
					t.Fatalf("commit seed notified claim: %v", err)
				}
				clock.Advance(delegateQuietWindow)
			}
			clock.Advance(elapsed)
			claim, err := controller.BeginQuietAttention(root, lease, clock.Now())
			if err != nil {
				t.Fatalf("BeginQuietAttention: %v", err)
			}
			return claim, controller, root, lease
		}

		first, firstController, firstRoot, firstLease := begin()
		second, secondController, _, _ := begin()
		firstID, secondID := "", ""
		if first != nil {
			firstID = first.attentionID
		}
		if second != nil {
			secondID = second.attentionID
		}
		if (first == nil) != (second == nil) || firstID != secondID {
			t.Fatalf("non-deterministic quiet admission: first=%q second=%q", firstID, secondID)
		}

		// Notification state no longer suppresses forever: a notified delegate
		// re-fires after one further full window, so admission depends only on
		// the elapsed quiet time from the active baseline.
		wantClaim := elapsed >= delegateQuietWindow
		if (first != nil) != wantClaim {
			t.Fatalf("quiet admission at %v with notified=%v: claim=%#v want=%v", elapsed, alreadyNotified, first, wantClaim)
		}
		if second != nil {
			if err := secondController.CompleteQuietAttention(second, false); err != nil {
				t.Fatalf("abort duplicate harness claim: %v", err)
			}
		}
		if first == nil {
			return
		}
		wantSequence := uint64(1)
		if alreadyNotified {
			wantSequence = 2
		}
		if want := delegateQuietAttentionIDForStretch(firstLease, wantSequence); first.attentionID != want {
			t.Fatalf("quiet attention id = %q, want %q", first.attentionID, want)
		}
		// Probe at the instant the first claim was actually admitted: for the
		// notified case that instant is one window past the seed wake, so it
		// clears the repeat baseline just as a real boundary tick would.
		duplicate, err := firstController.BeginQuietAttention(firstRoot, firstLease, first.notifiedAt)
		if err != nil || duplicate != nil {
			t.Fatalf("outstanding quiet claim admitted duplicate=%#v err=%v", duplicate, err)
		}
		if err := firstController.CompleteQuietAttention(first, false); err != nil {
			t.Fatalf("abort quiet claim: %v", err)
		}
		retry, err := firstController.BeginQuietAttention(firstRoot, firstLease, first.notifiedAt)
		if err != nil || retry == nil {
			t.Fatalf("retry quiet admission = %#v, %v", retry, err)
		}
		if retry.attentionID != first.attentionID {
			t.Fatalf("retry quiet attention id = %q, want %q", retry.attentionID, first.attentionID)
		}
		if err := firstController.CompleteQuietAttention(retry, false); err != nil {
			t.Fatalf("abort retried quiet claim: %v", err)
		}
	})
}
