package agent

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestStoppedGenerationFinishEventIsTheSingleHome pins the stop-finish decision
// that the generation finish reducer and the start-failure and recovery
// finishers now share. Rebuilding the stopped outcome, reason, packet, or
// delivery ID by hand at any site is how the copies drift, so this is the
// regression test for the fold. It fails to build before the shared home
// exists.
func TestStoppedGenerationFinishEventIsTheSingleHome(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_target", generation: 3}
	endedAt := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	wantDelivery := "dlg_target/delivery/3"

	t.Run("synthetic stopped packet", func(t *testing.T) {
		event, deliveryID := stoppedGenerationFinishEvent(lease, nil, endedAt)
		if deliveryID != wantDelivery {
			t.Fatalf("deliveryID = %q, want %q", deliveryID, wantDelivery)
		}
		assertStoppedGenerationFinishEvent(t, event, lease, endedAt, wantDelivery, delegateStoppedTerminalPacket())
	})

	t.Run("run-loop packet is carried", func(t *testing.T) {
		supplied := delegateTerminalErrorPacket("cancelled on the way out")
		event, _ := stoppedGenerationFinishEvent(lease, &supplied, endedAt)
		assertStoppedGenerationFinishEvent(t, event, lease, endedAt, wantDelivery, supplied)
	})
}

func assertStoppedGenerationFinishEvent(t *testing.T, event delegatestore.Event, lease delegateLease, endedAt time.Time, deliveryID string, packet delegatestore.TerminalPacket) {
	t.Helper()
	if event.Kind != delegatestore.EventDelegateRunFinished || event.DelegateID != lease.delegateID || event.RunFinished == nil {
		t.Fatalf("event = %#v, want a RunFinished for %s", event, lease.delegateID)
	}
	finished := event.RunFinished
	if finished.Generation != lease.generation ||
		finished.Outcome.Status != delegatestore.OutcomeStopped ||
		finished.Outcome.Reason != "stopped_by_parent" ||
		!finished.Outcome.EndedAt.Equal(endedAt) ||
		finished.Disposition != delegatestore.DispositionTerminalError ||
		finished.DeliveryID != deliveryID {
		t.Fatalf("finished = %#v, want stopped/terminal_error/stopped_by_parent at %v", finished, endedAt)
	}
	if finished.Packet == nil || !reflect.DeepEqual(*finished.Packet, packet) {
		t.Fatalf("finished.Packet = %#v, want %#v", finished.Packet, packet)
	}
}

// TestReduceStoppedGenerationFinishIntentCarriesReleaseAndLatch pins that the
// stop-finish decision asks for the generation release and carries the site's
// append-failure latch shape, so a site cannot silently drop either.
func TestReduceStoppedGenerationFinishIntentCarriesReleaseAndLatch(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	decision := c.reduceStoppedGenerationFinishIntent(finishIntent{lease: lease, latch: finishLatchRecoveryOnly})
	if len(decision.events) != 1 {
		t.Fatalf("events = %#v, want one RunFinished", decision.events)
	}
	if !decision.releaseGeneration || decision.deliveryID != "dlg_target/delivery/1" || decision.latch != finishLatchRecoveryOnly {
		t.Fatalf("decision = %#v, want releaseGeneration with delivery ID and recovery-only latch", decision)
	}
}

// TestSettledGenerationFinishMatchesPreFoldNormalization pins that the shared
// prepared-terminal normalization reproduces, input for input, the outcome
// selection the generation finish's Settling branch used before the fold. The
// reference is an independent copy of that pre-fold logic, so any divergence in
// the shared home fails here.
func TestSettledGenerationFinishMatchesPreFoldNormalization(t *testing.T) {
	resumable := true
	terminalErrorMetadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		Outcome:    delegatestore.OutcomeFailed,
		Reason:     "runtime_exploded",
		RunEndedAt: "2026-09-28T11:59:00Z",
	})
	exhaustedMetadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		Outcome:             delegatestore.OutcomeExhausted,
		RunEndedAt:          "2026-09-28T11:59:00Z",
		ExhaustionBudget:    delegatestore.ExhaustionBudgetToolRounds,
		ExhaustionLimit:     7,
		ExhaustionResumable: &resumable,
	})
	reportedMetadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		Outcome:    delegatestore.OutcomeCompleted,
		RunEndedAt: "2026-09-28T11:58:00Z",
	})
	terminalError := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Metadata: terminalErrorMetadata}
	exhausted := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Metadata: exhaustedMetadata}
	reported := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Metadata: reportedMetadata}

	cases := []struct {
		name     string
		prepared delegatestore.TerminalPacket
		incoming delegateFinish
	}{
		{"reported", reported, delegateFinish{}},
		{"missing terminal", delegateMissingTerminalPacket(), delegateFinish{}},
		{"terminal error takes incoming failure", terminalError, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "run_loop_failed"}},
		{"terminal error ignores incoming completion", terminalError, delegateFinish{outcome: delegatestore.OutcomeCompleted}},
		{"terminal error ignores empty incoming", terminalError, delegateFinish{}},
		{"exhausted carries through", exhausted, delegateFinish{}},
		{"exhausted outranks incoming failure", exhausted, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "run_loop_failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := settledGenerationFinish(tc.prepared, tc.incoming)
			wantMeta, wantOutcome, wantDisposition, wantReason := preFoldSettledGenerationFinish(tc.prepared, tc.incoming)
			if !reflect.DeepEqual(got.meta, wantMeta) {
				t.Fatalf("meta = %#v, want %#v", got.meta, wantMeta)
			}
			if got.outcome != wantOutcome || got.disposition != wantDisposition || got.reason != wantReason {
				t.Fatalf("outcome/disposition/reason = %q/%q/%q, want %q/%q/%q",
					got.outcome, got.disposition, got.reason, wantOutcome, wantDisposition, wantReason)
			}
			if !reflect.DeepEqual(got.prepared, delegatePreparedFinish(tc.prepared)) {
				t.Fatalf("prepared = %#v, want delegatePreparedFinish result", got.prepared)
			}
		})
	}
}

// preFoldSettledGenerationFinish is the generation finish's Settling-branch
// normalization as it read before the fold. It is the independent reference the
// shared home must reproduce.
func preFoldSettledGenerationFinish(prepared delegatestore.TerminalPacket, incoming delegateFinish) (delegateFinish, delegatestore.OutcomeStatus, delegatestore.RunDisposition, string) {
	finish := incoming
	preparedFinish := delegatePreparedFinish(prepared)
	outcome, disposition, reason := preparedFinish.outcome, preparedFinish.disposition, preparedFinish.reason
	if prepared.Kind == delegatestore.PacketTerminalError && !delegateIsMissingTerminalPacket(prepared) &&
		incoming.outcome != "" && incoming.outcome != delegatestore.OutcomeCompleted {
		outcome = incoming.outcome
		disposition = delegatestore.DispositionTerminalError
		reason = incoming.reason
	} else if preparedFinish.outcome == delegatestore.OutcomeExhausted {
		finish = preparedFinish
	}
	return finish, outcome, disposition, reason
}
