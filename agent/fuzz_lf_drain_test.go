//go:build evenerfuzz

package agent

import (
	"strings"
	"testing"
)

// This file fuzzes selectDrainNextAction — the pure priority decision lifted out
// of processInputKindWithProvenance's drain loop. The live loop buries the
// decision under pops (popFollowUp/popQueueHead), a store-backed goal-gate fold
// (armGoalContinuation), and a notification peek; the extracted core takes only
// the RESULTS of those side effects so the priority ladder can be fuzzed directly.
//
// Oracles: determinism; the action is always one of the six valid values;
// skipGoalGate matches its definition; the priority ladder is honored; and the
// goal accounting invariants hold — the gate never re-arms and a notification is
// never selected right after a notification turn.
//
// The lf_ prefix marks helpers owned by this refactor/fuzz lane.

func lf_drainActionValid(a drainAction) bool {
	switch a {
	case runFollowUp, runQueued, armGoalGate, runNotification, runDeferredContInline, goIdle:
		return true
	}
	return false
}

func FuzzLfSelectDrainNextAction(f *testing.F) {
	// Seeds hitting distinct ladder rungs.
	f.Add(uint8(0), false, "follow", "queued", 0, 0, true, false, false) // follow-up wins
	f.Add(uint8(0), false, "", "queued", 0, 0, true, false, false)       // queued wins
	f.Add(uint8(1), false, "", "", 2, 0, true, false, false)             // queued via images
	f.Add(uint8(1), false, "", "", 0, 0, true, false, false)             // notification (ranKind != notif)
	f.Add(uint8(1), false, "", "", 0, 0, false, false, false)            // gate eligible => armGoalGate
	f.Add(uint8(1), true, "", "", 0, 0, false, false, false)             // already deferred => runDeferredContInline
	f.Add(uint8(2), false, "", "", 0, 0, true, false, false)             // ranKind==notif, pending ignored => goIdle
	f.Add(uint8(2), true, "", "", 0, 0, false, false, false)             // ranKind==notif, deferred => runDeferredContInline
	f.Add(uint8(1), false, "", "", 0, 0, true, true, false)              // the steering carrier is queued input: outranks the notification
	f.Add(uint8(0), false, "follow", "", 0, 0, false, true, false)       // follow-up outranks the carrier
	f.Add(uint8(1), false, "", "", 0, 0, true, false, true)              // parked steering: no autonomous turn, idle
	f.Add(uint8(0), false, "", "queued", 0, 0, true, false, true)        // parked steering still runs a queued message
	f.Add(uint8(1), false, "", "", 0, 1, true, false, false)             // queued via a skill-only entry: outranks the notification
	f.Add(uint8(1), false, "", "", 0, 1, false, false, false)            // skill-only entry runs instead of the goal gate

	f.Fuzz(func(t *testing.T, kindSel uint8, haveDeferredCont bool,
		followUp, queuedText string, queuedImages, queuedSkills int, notificationsPending bool, queuedCarrier bool, steeringParked bool) {

		in := drainInputs{
			RanKind:              lf_entryKinds[int(kindSel)%len(lf_entryKinds)],
			HaveDeferredCont:     haveDeferredCont,
			FollowUp:             followUp,
			QueuedText:           queuedText,
			QueuedImages:         queuedImages,
			QueuedSkills:         queuedSkills,
			NotificationsPending: notificationsPending,
			QueuedCarrier:        queuedCarrier,
			SteeringParked:       steeringParked,
		}

		action, skip := selectDrainNextAction(in)

		// Determinism.
		if a2, s2 := selectDrainNextAction(in); a2 != action || s2 != skip {
			t.Fatalf("nondeterministic: (%v,%v) vs (%v,%v)", action, skip, a2, s2)
		}
		// Total function: exactly one valid action.
		if !lf_drainActionValid(action) {
			t.Fatalf("invalid action %v", action)
		}
		// skipGoalGate is exactly "ran a notification OR a continuation is already
		// deferred" — the goal gate folds only when neither holds.
		wantSkip := in.RanKind == EntryNotification || in.HaveDeferredCont || in.SteeringParked
		if skip != wantSkip {
			t.Fatalf("skipGoalGate=%v want %v (%+v)", skip, wantSkip, in)
		}

		hasFollowUp := strings.TrimSpace(in.FollowUp) != ""
		hasQueued := strings.TrimSpace(in.QueuedText) != "" || in.QueuedImages > 0 || in.QueuedSkills > 0 || in.QueuedCarrier

		// Priority ladder for the TURN that runs next: follow-up > queued (a
		// message, or the steering carrier) > notification > deferred-inline > idle. The goal-gate FOLD is a separate
		// side effect the wrapper performs whenever !skipGoalGate (before running the
		// notification turn), so "goal-gate > notification" holds at the fold level;
		// armGoalGate is returned only when the gate is eligible and no notification
		// preempts, i.e. the resulting turn depends on the fold result. Recompute the
		// expected action and require equality to pin the exact current order (the
		// highest-risk property).
		var want drainAction
		switch {
		case hasFollowUp:
			want = runFollowUp
		case hasQueued:
			want = runQueued
		case in.SteeringParked:
			want = goIdle
		case in.RanKind != EntryNotification && in.NotificationsPending:
			want = runNotification
		case !wantSkip:
			want = armGoalGate
		case in.HaveDeferredCont:
			want = runDeferredContInline
		default:
			want = goIdle
		}
		if action != want {
			t.Fatalf("action=%v want %v (%+v skip=%v)", action, want, in, skip)
		}

		// Per-action invariants (independent of the recomputation above).
		switch action {
		case runFollowUp:
			if !hasFollowUp {
				t.Fatalf("runFollowUp without a follow-up: %+v", in)
			}
		case runQueued:
			if hasFollowUp || !hasQueued {
				t.Fatalf("runQueued but hasFollowUp=%v hasQueued=%v", hasFollowUp, hasQueued)
			}
		case runNotification:
			// A notification is never selected right after a notification turn, and
			// only when one is actually pending.
			if in.RanKind == EntryNotification || !in.NotificationsPending {
				t.Fatalf("runNotification with RanKind=%v pending=%v", in.RanKind, in.NotificationsPending)
			}
		case armGoalGate:
			// The gate arms only when it is eligible: not after a notification turn
			// and not while a continuation is already deferred.
			if skip {
				t.Fatalf("armGoalGate while skipGoalGate: %+v", in)
			}
			if in.RanKind == EntryNotification || in.HaveDeferredCont {
				t.Fatalf("armGoalGate with RanKind=%v deferred=%v", in.RanKind, in.HaveDeferredCont)
			}
		case runDeferredContInline:
			if !in.HaveDeferredCont {
				t.Fatalf("runDeferredContInline without a deferred continuation: %+v", in)
			}
		}
	})
}
