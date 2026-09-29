package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A failed run's error text is bounded without splitting a character: a
// byte cut at the limit could leave half a rune, which the journal and the
// wire would then carry as invalid UTF-8.
func TestBoundedFinishTextKeepsWholeRunes(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", delegateFinishReasonLimit)
	got := boundedFinishText("  " + long + "  ")
	if !utf8.ValidString(got) || len(got) > delegateFinishReasonLimit || got == "" {
		t.Fatalf("bounded text: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if boundedFinishText("  provider returned 500 \n") != "provider returned 500" {
		t.Fatalf("short text should come back trimmed")
	}
}

// The run error's first line is the failed outcome's error, and the packet
// metadata carries it, so crash replay (delegatePreparedFinish) restores it.
func TestFailedRunCarriesItsErrorThroughReplay(t *testing.T) {
	t.Parallel()
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{runErr: errors.New("provider returned 500\nretry-after: 30")})
	if finish.outcome != delegatestore.OutcomeFailed || finish.errorText != "provider returned 500" {
		t.Fatalf("finish outcome=%q error=%q", finish.outcome, finish.errorText)
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(finish.packet.Metadata, &metadata); err != nil || metadata.Error != "provider returned 500" {
		t.Fatalf("metadata error=%q (%v)", metadata.Error, err)
	}
	if replayed := delegatePreparedFinish(*finish.packet); replayed.errorText != "provider returned 500" {
		t.Fatalf("replayed error=%q", replayed.errorText)
	}
	reported := stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "done", communicated: true})
	if reported.errorText != "" {
		t.Fatalf("a reported run has no error, got %q", reported.errorText)
	}
}

// The controller stamps a failed finish's error on the RunFinished event it
// appends, as it stamps an exhausted one's budget.
func TestFinishEventsCarryAFailedRunsError(t *testing.T) {
	t.Parallel()
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	finish := delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "failed", errorText: "provider returned 500"}
	events := delegateFinishMetadataEvents(
		[]delegatestore.Event{delegateRunFinishedEvent(lease, finish.outcome, delegatestore.DispositionTerminalError, finish.reason, time.Time{}, "", nil)},
		lease, finish, finish.outcome, finish.reason,
	)
	if got := events[0].RunFinished.Outcome.Error; got != "provider returned 500" {
		t.Fatalf("finish event error=%q", got)
	}
	stopped := delegateFinishMetadataEvents(
		[]delegatestore.Event{delegateRunFinishedEvent(lease, delegatestore.OutcomeCancelled, delegatestore.DispositionTerminalError, "cancelled", time.Time{}, "", nil)},
		lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, errorText: "context canceled"}, delegatestore.OutcomeCancelled, "cancelled",
	)
	if got := stopped[0].RunFinished.Outcome.Error; got != "" {
		t.Fatalf("a cancelled finish carries no error, got %q", got)
	}
}
