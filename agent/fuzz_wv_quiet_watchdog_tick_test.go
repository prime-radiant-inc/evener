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
//   - an already-notified stretch never admits another attention;
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
			controller.mu.Lock()
			controller.live[lease.delegateID].quietNotified = alreadyNotified
			controller.mu.Unlock()
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

		wantClaim := elapsed >= delegateQuietWindow && !alreadyNotified
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
		if first.attentionID != delegateQuietAttentionID(firstLease) {
			t.Fatalf("quiet attention id = %q, want %q", first.attentionID, delegateQuietAttentionID(firstLease))
		}
		duplicate, err := firstController.BeginQuietAttention(firstRoot, firstLease, first.activityAt.Add(elapsed))
		if err != nil || duplicate != nil {
			t.Fatalf("outstanding quiet claim admitted duplicate=%#v err=%v", duplicate, err)
		}
		if err := firstController.CompleteQuietAttention(first, false); err != nil {
			t.Fatalf("abort quiet claim: %v", err)
		}
		retry, err := firstController.BeginQuietAttention(firstRoot, firstLease, first.activityAt.Add(elapsed))
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
